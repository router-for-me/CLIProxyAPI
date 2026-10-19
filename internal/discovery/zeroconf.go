package discovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/zeroconf/v2"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	maxDNSLabelBytes         = 63
	maxHostCollisionAttempts = 8
	mdnsCacheFlushBit        = 1 << 15
	mdnsServiceRecordTTL     = 3200
	mdnsHostRecordTTL        = 120
	mdnsLegacyRecordTTL      = 10
	mdnsPort                 = 5353
	mdnsHostProbeCount       = 3
	mdnsHostProbeInterval    = 250 * time.Millisecond
	mdnsAnnounceRepetitions  = 2
	mdnsAnnounceInterval     = time.Second
	dnsTypeNSEC              = dnsmessage.Type(47)
)

var (
	mdnsGroupAddrIPv4    = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}
	mdnsGroupAddrIPv6    = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: mdnsPort}
	mdnsWildcardAddrIPv4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 0), Port: mdnsPort}
	mdnsWildcardAddrIPv6 = &net.UDPAddr{IP: net.ParseIP("ff02::"), Port: mdnsPort}
)

type zeroconfServer interface {
	Shutdown()
}

type registerProxyFunc func(instance, service, domain string, port int, host string, ips []string, text []string, ifaces []net.Interface, opts ...zeroconf.ServerOption) (zeroconfServer, error)
type hostConflictProbeFunc func(ctx context.Context, hostFQDN string, ifaces []net.Interface, localIPs []string) (bool, error)

func validateRegisterProxyArgs(instance, service, domain string, port int, host string, ips []string) error {
	if strings.TrimSpace(instance) == "" {
		return fmt.Errorf("missing service instance name")
	}
	if strings.TrimSpace(service) == "" {
		return fmt.Errorf("missing service name")
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("missing host name")
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	for _, ip := range ips {
		if net.ParseIP(strings.TrimSpace(ip)) == nil {
			return fmt.Errorf("failed to parse given IP: %v", ip)
		}
	}
	return nil
}

func defaultRegisterProxy(instance, service, domain string, port int, host string, ips []string, text []string, ifaces []net.Interface, _ ...zeroconf.ServerOption) (zeroconfServer, error) {
	if errValid := validateRegisterProxyArgs(instance, service, domain, port, host, ips); errValid != nil {
		return nil, errValid
	}
	resp, err := newHostRecordResponderWithRebind(nil, host, domain, ips, ifaces, nil)
	if err != nil {
		return nil, err
	}
	if hr, ok := resp.(*hostRecordResponder); ok && hr != nil {
		hr.configureService(instance, service, domain, port, text)
		hr.announceService()
	}
	return resp, nil
}

func formatHostFQDN(host, domain string) string {
	h := strings.TrimSuffix(strings.TrimSpace(host), ".")
	d := strings.Trim(strings.TrimSpace(domain), ".")
	if d == "" {
		d = "local"
	}
	if !strings.HasSuffix(strings.ToLower(h), "."+strings.ToLower(d)) {
		h = h + "." + d
	}
	return h + "."
}

func splitIPv4AndIPv6(ips []string) (v4 [][4]byte, v6 [][16]byte) {
	for _, raw := range ips {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			v4 = append(v4, [4]byte(ip4))
			continue
		}
		if ip16 := ip.To16(); ip16 != nil {
			v6 = append(v6, [16]byte(ip16))
		}
	}
	return v4, v6
}

func packWireDomainName(fqdn string) []byte {
	trimmed := strings.Trim(strings.TrimSpace(fqdn), ".")
	if trimmed == "" {
		return []byte{0}
	}
	parts := strings.Split(trimmed, ".")
	var out []byte
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxDNSLabelBytes {
			return nil
		}
		out = append(out, byte(len(part)))
		out = append(out, part...)
	}
	out = append(out, 0)
	return out
}

func buildHostNSECRData(hostFQDN string, hasA, hasAAAA bool) []byte {
	wireName := packWireDomainName(hostFQDN)
	if len(wireName) == 0 || (!hasA && !hasAAAA) {
		return nil
	}
	var bitmap [4]byte
	bitmapLen := 0
	if hasA {
		bitmap[0] |= 1 << (7 - 1) // TypeA = 1
		bitmapLen = 1
	}
	if hasAAAA {
		bitmap[3] |= 1 << (7 - (28 - 24)) // TypeAAAA = 28
		bitmapLen = 4
	}
	out := append(wireName, 0x00, byte(bitmapLen))
	return append(out, bitmap[:bitmapLen]...)
}

func buildHostAddressReply(query []byte, hostFQDN string, ips []string, legacyUnicast bool) (reply []byte, unicast bool) {
	return buildHostAddressReplyFiltered(query, hostFQDN, ips, legacyUnicast, nil)
}

func buildHostAddressReplyFiltered(query []byte, hostFQDN string, ips []string, legacyUnicast bool, onlyQU *bool) (reply []byte, unicast bool) {
	normQuery := rewriteWireLabelChar(query, '.', dotInLabelSentinel)
	var p dnsmessage.Parser
	hdr, err := p.Start(normQuery)
	if err != nil || hdr.Response {
		return nil, false
	}
	questions, errQ := p.AllQuestions()
	if errQ != nil || len(questions) == 0 {
		return nil, false
	}
	if errSkipAns := p.SkipAllAnswers(); errSkipAns != nil {
		return nil, false
	}
	if errSkipAuth := p.SkipAllAuthorities(); errSkipAuth != nil {
		return nil, false
	}

	var wantA, wantAAAA, extraA, extraAAAA bool
	var matchedQuestions []dnsmessage.Question
	seenQ := make(map[string]struct{}, len(questions))
	for _, q := range questions {
		isQU := uint16(q.Class)&mdnsCacheFlushBit != 0
		if onlyQU != nil && isQU != *onlyQU {
			continue
		}
		if !strings.EqualFold(q.Name.String(), hostFQDN) {
			continue
		}
		baseClass := uint16(q.Class) &^ mdnsCacheFlushBit
		if baseClass != uint16(dnsmessage.ClassINET) && baseClass != uint16(dnsmessage.ClassANY) {
			continue
		}
		switch q.Type {
		case dnsmessage.TypeA:
			wantA = true
			extraAAAA = true
		case dnsmessage.TypeAAAA:
			wantAAAA = true
			extraA = true
		case dnsmessage.TypeALL:
			wantA = true
			wantAAAA = true
		default:
			continue
		}
		qKey := fmt.Sprintf("%s\x00%d\x00%d", strings.ToLower(q.Name.String()), q.Type, baseClass)
		if _, dup := seenQ[qKey]; !dup {
			seenQ[qKey] = struct{}{}
			matchedQuestions = append(matchedQuestions, dnsmessage.Question{
				Name:  q.Name,
				Type:  q.Type,
				Class: dnsmessage.Class(baseClass),
			})
		}
		if isQU {
			unicast = true
		}
	}
	if !wantA && !wantAAAA {
		return nil, false
	}
	if legacyUnicast {
		unicast = true
	}

	v4, v6 := splitIPv4AndIPv6(ips)
	if len(v4) == 0 && len(v6) == 0 {
		return nil, false
	}
	hasAnswerA := wantA && len(v4) > 0
	hasAnswerAAAA := wantAAAA && len(v6) > 0
	needNSEC := (wantA && len(v4) == 0) || (wantAAAA && len(v6) == 0)

	name, errName := dnsmessage.NewName(hostFQDN)
	if errName != nil {
		return nil, false
	}
	rrClass := dnsmessage.ClassINET | dnsmessage.Class(mdnsCacheFlushBit)
	ttl := uint32(mdnsHostRecordTTL)
	if legacyUnicast {
		rrClass = dnsmessage.ClassINET
		ttl = mdnsLegacyRecordTTL
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:            hdr.ID,
		Response:      true,
		Authoritative: true,
	})
	builder.EnableCompression()
	if legacyUnicast && len(matchedQuestions) > 0 {
		if errStartQ := builder.StartQuestions(); errStartQ != nil {
			return nil, false
		}
		for _, mq := range matchedQuestions {
			if errQAdd := builder.Question(mq); errQAdd != nil {
				return nil, false
			}
		}
	}
	if errAns := builder.StartAnswers(); errAns != nil {
		return nil, false
	}
	if hasAnswerA {
		for _, addr := range v4 {
			if errAdd := builder.AResource(dnsmessage.ResourceHeader{
				Name:  name,
				Type:  dnsmessage.TypeA,
				Class: rrClass,
				TTL:   ttl,
			}, dnsmessage.AResource{A: addr}); errAdd != nil {
				return nil, false
			}
		}
	}
	if hasAnswerAAAA {
		for _, addr := range v6 {
			if errAdd := builder.AAAAResource(dnsmessage.ResourceHeader{
				Name:  name,
				Type:  dnsmessage.TypeAAAA,
				Class: rrClass,
				TTL:   ttl,
			}, dnsmessage.AAAAResource{AAAA: addr}); errAdd != nil {
				return nil, false
			}
		}
	}
	if needNSEC {
		nsecData := buildHostNSECRData(hostFQDN, len(v4) > 0, len(v6) > 0)
		if len(nsecData) == 0 {
			return nil, false
		}
		if errNSEC := builder.UnknownResource(dnsmessage.ResourceHeader{
			Name:  name,
			Type:  dnsTypeNSEC,
			Class: rrClass,
			TTL:   ttl,
		}, dnsmessage.UnknownResource{
			Type: dnsTypeNSEC,
			Data: nsecData,
		}); errNSEC != nil {
			return nil, false
		}
	}

	addExtraA := !hasAnswerA && extraA && len(v4) > 0
	addExtraAAAA := !hasAnswerAAAA && extraAAAA && len(v6) > 0
	if addExtraA || addExtraAAAA {
		if errExt := builder.StartAdditionals(); errExt != nil {
			return nil, false
		}
		if addExtraA {
			for _, addr := range v4 {
				_ = builder.AResource(dnsmessage.ResourceHeader{
					Name:  name,
					Type:  dnsmessage.TypeA,
					Class: rrClass,
					TTL:   ttl,
				}, dnsmessage.AResource{A: addr})
			}
		}
		if addExtraAAAA {
			for _, addr := range v6 {
				_ = builder.AAAAResource(dnsmessage.ResourceHeader{
					Name:  name,
					Type:  dnsmessage.TypeAAAA,
					Class: rrClass,
					TTL:   ttl,
				}, dnsmessage.AAAAResource{AAAA: addr})
			}
		}
	}

	out, errFinish := builder.Finish()
	if errFinish != nil {
		return nil, false
	}
	return rewriteWireLabelChar(out, dotInLabelSentinel, '.'), unicast
}

func buildHostGoodbyePacket(hostFQDN string, ips []string) []byte {
	v4, v6 := splitIPv4AndIPv6(ips)
	if len(v4) == 0 && len(v6) == 0 {
		return nil
	}
	name, errName := dnsmessage.NewName(hostFQDN)
	if errName != nil {
		return nil
	}
	rrClass := dnsmessage.ClassINET | dnsmessage.Class(mdnsCacheFlushBit)
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		Response:      true,
		Authoritative: true,
	})
	builder.EnableCompression()
	if errAns := builder.StartAnswers(); errAns != nil {
		return nil
	}
	for _, addr := range v4 {
		if errAdd := builder.AResource(dnsmessage.ResourceHeader{
			Name:  name,
			Type:  dnsmessage.TypeA,
			Class: rrClass,
			TTL:   0,
		}, dnsmessage.AResource{A: addr}); errAdd != nil {
			return nil
		}
	}
	for _, addr := range v6 {
		if errAdd := builder.AAAAResource(dnsmessage.ResourceHeader{
			Name:  name,
			Type:  dnsmessage.TypeAAAA,
			Class: rrClass,
			TTL:   0,
		}, dnsmessage.AAAAResource{AAAA: addr}); errAdd != nil {
			return nil
		}
	}
	out, errFinish := builder.Finish()
	if errFinish != nil {
		return nil
	}
	return out
}

const dotInLabelSentinel byte = '\x01'

type mdnsServiceConfig struct {
	serviceTypeName string
	serviceName     string
	instanceName    string
	subtypes        []string
	port            uint16
	text            []string
}

func rewriteWireName(buf []byte, off int, fromByte, toByte byte) int {
	for off < len(buf) {
		b := buf[off]
		if b == 0 {
			return off + 1
		}
		if b&0xC0 == 0xC0 {
			if off+2 > len(buf) {
				return -1
			}
			return off + 2
		}
		if b >= 64 {
			return -1
		}
		end := off + 1 + int(b)
		if end > len(buf) {
			return -1
		}
		for i := off + 1; i < end; i++ {
			if buf[i] == fromByte {
				buf[i] = toByte
			}
		}
		off = end
	}
	return -1
}

func rewriteWireLabelChar(pkt []byte, fromByte, toByte byte) []byte {
	if len(pkt) < 12 {
		return pkt
	}
	buf := bytes.Clone(pkt)
	qd := int(binary.BigEndian.Uint16(buf[4:6]))
	an := int(binary.BigEndian.Uint16(buf[6:8]))
	ns := int(binary.BigEndian.Uint16(buf[8:10]))
	ar := int(binary.BigEndian.Uint16(buf[10:12]))
	off := 12
	for i := 0; i < qd; i++ {
		off = rewriteWireName(buf, off, fromByte, toByte)
		if off < 0 || off+4 > len(buf) {
			return buf
		}
		off += 4
	}
	totalRR := an + ns + ar
	for i := 0; i < totalRR; i++ {
		off = rewriteWireName(buf, off, fromByte, toByte)
		if off < 0 || off+10 > len(buf) {
			return buf
		}
		rrType := dnsmessage.Type(binary.BigEndian.Uint16(buf[off : off+2]))
		rdLen := int(binary.BigEndian.Uint16(buf[off+8 : off+10]))
		rdataStart := off + 10
		rdataEnd := rdataStart + rdLen
		if rdataEnd > len(buf) {
			return buf
		}
		switch rrType {
		case dnsmessage.TypePTR, dnsmessage.TypeCNAME, dnsmessage.TypeNS:
			_ = rewriteWireName(buf[:rdataEnd], rdataStart, fromByte, toByte)
		case dnsmessage.TypeSRV:
			if rdLen >= 6 {
				_ = rewriteWireName(buf[:rdataEnd], rdataStart+6, fromByte, toByte)
			}
		}
		off = rdataEnd
	}
	return buf
}

func parseMDNSServiceConfig(instance, service, domain string, port int, text []string) mdnsServiceConfig {
	cleanDom := strings.Trim(strings.TrimSpace(domain), ".")
	if cleanDom == "" {
		cleanDom = "local"
	}
	parts := strings.Split(service, ",")
	baseSvc := strings.Trim(strings.TrimSpace(parts[0]), ".")
	svcName := baseSvc + "." + cleanDom + "."
	cleanInst := strings.ReplaceAll(strings.Trim(strings.TrimSpace(instance), "."), ".", string(dotInLabelSentinel))
	var subs []string
	for _, sub := range parts[1:] {
		s := strings.Trim(strings.TrimSpace(sub), ".")
		if s != "" {
			subs = append(subs, s+"._sub."+svcName)
		}
	}
	return mdnsServiceConfig{
		serviceTypeName: "_services._dns-sd._udp." + cleanDom + ".",
		serviceName:     svcName,
		instanceName:    cleanInst + "." + svcName,
		subtypes:        subs,
		port:            uint16(port),
		text:            append([]string(nil), text...),
	}
}

func appendHostAddrResources(builder *dnsmessage.Builder, hostName dnsmessage.Name, rrClass dnsmessage.Class, ttl uint32, v4 [][4]byte, v6 [][16]byte) error {
	for _, addr := range v4 {
		if err := builder.AResource(dnsmessage.ResourceHeader{
			Name:  hostName,
			Type:  dnsmessage.TypeA,
			Class: rrClass,
			TTL:   ttl,
		}, dnsmessage.AResource{A: addr}); err != nil {
			return err
		}
	}
	for _, addr := range v6 {
		if err := builder.AAAAResource(dnsmessage.ResourceHeader{
			Name:  hostName,
			Type:  dnsmessage.TypeAAAA,
			Class: rrClass,
			TTL:   ttl,
		}, dnsmessage.AAAAResource{AAAA: addr}); err != nil {
			return err
		}
	}
	return nil
}

func buildServicePacket(svc mdnsServiceConfig, hostFQDN string, ips []string, ttl uint32) []byte {
	v4, v6 := splitIPv4AndIPv6(ips)
	if svc.serviceName == "" {
		if ttl == 0 {
			return buildHostGoodbyePacket(hostFQDN, ips)
		}
		return nil
	}
	svcTypeName, err1 := dnsmessage.NewName(svc.serviceTypeName)
	svcName, err2 := dnsmessage.NewName(svc.serviceName)
	instName, err3 := dnsmessage.NewName(svc.instanceName)
	hostName, err4 := dnsmessage.NewName(hostFQDN)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return nil
	}
	var subNames []dnsmessage.Name
	for _, sub := range svc.subtypes {
		if sn, errS := dnsmessage.NewName(sub); errS == nil {
			subNames = append(subNames, sn)
		}
	}

	svcTTL := ttl
	hostTTL := ttl
	if ttl > 0 {
		svcTTL = mdnsServiceRecordTTL
		hostTTL = mdnsHostRecordTTL
	}
	flushClass := dnsmessage.ClassINET | dnsmessage.Class(mdnsCacheFlushBit)
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		Response:      true,
		Authoritative: true,
	})
	builder.EnableCompression()
	if errAns := builder.StartAnswers(); errAns != nil {
		return nil
	}
	if err := builder.SRVResource(dnsmessage.ResourceHeader{
		Name:  instName,
		Type:  dnsmessage.TypeSRV,
		Class: flushClass,
		TTL:   svcTTL,
	}, dnsmessage.SRVResource{Port: svc.port, Target: hostName}); err != nil {
		return nil
	}
	if err := builder.TXTResource(dnsmessage.ResourceHeader{
		Name:  instName,
		Type:  dnsmessage.TypeTXT,
		Class: flushClass,
		TTL:   svcTTL,
	}, dnsmessage.TXTResource{TXT: svc.text}); err != nil {
		return nil
	}
	if err := builder.PTRResource(dnsmessage.ResourceHeader{
		Name:  svcName,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
		TTL:   svcTTL,
	}, dnsmessage.PTRResource{PTR: instName}); err != nil {
		return nil
	}
	if err := builder.PTRResource(dnsmessage.ResourceHeader{
		Name:  svcTypeName,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
		TTL:   svcTTL,
	}, dnsmessage.PTRResource{PTR: svcName}); err != nil {
		return nil
	}
	for _, sn := range subNames {
		if err := builder.PTRResource(dnsmessage.ResourceHeader{
			Name:  sn,
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
			TTL:   svcTTL,
		}, dnsmessage.PTRResource{PTR: instName}); err != nil {
			return nil
		}
	}
	if err := appendHostAddrResources(&builder, hostName, flushClass, hostTTL, v4, v6); err != nil {
		return nil
	}
	out, errFinish := builder.Finish()
	if errFinish != nil {
		return nil
	}
	return rewriteWireLabelChar(out, dotInLabelSentinel, '.')
}

func ptrKnownKey(ownerName, targetName string) string {
	return strings.ToLower(ownerName) + "\x00" + strings.ToLower(targetName)
}

func collectKnownPTRAnswers(p *dnsmessage.Parser, replyTTL uint32) (map[string]struct{}, error) {
	var known map[string]struct{}
	for {
		rh, errH := p.AnswerHeader()
		if errH != nil {
			if errH == dnsmessage.ErrSectionDone {
				return known, nil
			}
			return nil, errH
		}
		if rh.Type != dnsmessage.TypePTR || rh.TTL < replyTTL/2 {
			if errSkip := p.SkipAnswer(); errSkip != nil {
				return nil, errSkip
			}
			continue
		}
		ptr, errPTR := p.PTRResource()
		if errPTR != nil {
			return nil, errPTR
		}
		if known == nil {
			known = make(map[string]struct{})
		}
		known[ptrKnownKey(rh.Name.String(), ptr.PTR.String())] = struct{}{}
	}
}

func isKnownPTRAnswer(known map[string]struct{}, ownerName, targetName string) bool {
	if len(known) == 0 {
		return false
	}
	_, ok := known[ptrKnownKey(ownerName, targetName)]
	return ok
}

func buildServiceQueryReply(query []byte, svc mdnsServiceConfig, hostFQDN string, ips []string, legacyUnicast bool) (reply []byte, unicast bool) {
	return buildServiceQueryReplyFiltered(query, svc, hostFQDN, ips, legacyUnicast, nil)
}

func buildServiceQueryReplyFiltered(query []byte, svc mdnsServiceConfig, hostFQDN string, ips []string, legacyUnicast bool, onlyQU *bool) (reply []byte, unicast bool) {
	if svc.serviceName == "" {
		return nil, false
	}
	normQuery := rewriteWireLabelChar(query, '.', dotInLabelSentinel)
	var p dnsmessage.Parser
	hdr, err := p.Start(normQuery)
	if err != nil || hdr.Response {
		return nil, false
	}
	questions, errQ := p.AllQuestions()
	if errQ != nil || len(questions) == 0 {
		return nil, false
	}

	svcTTL := uint32(mdnsServiceRecordTTL)
	hostTTL := uint32(mdnsHostRecordTTL)
	rrClass := dnsmessage.ClassINET | dnsmessage.Class(mdnsCacheFlushBit)
	if legacyUnicast {
		svcTTL = mdnsLegacyRecordTTL
		hostTTL = mdnsLegacyRecordTTL
		rrClass = dnsmessage.ClassINET
		unicast = true
	}

	knownPTRs, errKnown := collectKnownPTRAnswers(&p, svcTTL)
	if errKnown != nil {
		return nil, false
	}

	svcTypeName, err1 := dnsmessage.NewName(svc.serviceTypeName)
	svcName, err2 := dnsmessage.NewName(svc.serviceName)
	instName, err3 := dnsmessage.NewName(svc.instanceName)
	hostName, err4 := dnsmessage.NewName(hostFQDN)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return nil, false
	}

	var matchedQuestions []dnsmessage.Question
	var answerTypeEnum, answerBrowse, answerLookup bool
	var browseSubtypes []dnsmessage.Name
	seenSubtypes := make(map[string]struct{}, len(svc.subtypes))
	seenQ := make(map[string]struct{}, len(questions))

	for _, q := range questions {
		isQU := uint16(q.Class)&mdnsCacheFlushBit != 0
		if onlyQU != nil && isQU != *onlyQU {
			continue
		}
		baseClass := uint16(q.Class) &^ mdnsCacheFlushBit
		if baseClass != uint16(dnsmessage.ClassINET) && baseClass != uint16(dnsmessage.ClassANY) {
			continue
		}
		qNameStr := q.Name.String()
		switch {
		case strings.EqualFold(qNameStr, svc.serviceTypeName):
			if q.Type != dnsmessage.TypePTR && q.Type != dnsmessage.TypeALL {
				continue
			}
			if isKnownPTRAnswer(knownPTRs, svc.serviceTypeName, svc.serviceName) {
				continue
			}
			answerTypeEnum = true
		case strings.EqualFold(qNameStr, svc.serviceName):
			if q.Type != dnsmessage.TypePTR && q.Type != dnsmessage.TypeALL {
				continue
			}
			if isKnownPTRAnswer(knownPTRs, svc.serviceName, svc.instanceName) {
				continue
			}
			answerBrowse = true
		case strings.EqualFold(qNameStr, svc.instanceName):
			if q.Type != dnsmessage.TypeSRV && q.Type != dnsmessage.TypeTXT && q.Type != dnsmessage.TypePTR && q.Type != dnsmessage.TypeALL {
				continue
			}
			answerLookup = true
		default:
			matchedSub := ""
			for _, sub := range svc.subtypes {
				if strings.EqualFold(qNameStr, sub) {
					matchedSub = sub
					break
				}
			}
			if matchedSub == "" || (q.Type != dnsmessage.TypePTR && q.Type != dnsmessage.TypeALL) {
				continue
			}
			if isKnownPTRAnswer(knownPTRs, matchedSub, svc.instanceName) {
				continue
			}
			if _, dupSub := seenSubtypes[matchedSub]; !dupSub {
				seenSubtypes[matchedSub] = struct{}{}
				if sn, errSN := dnsmessage.NewName(matchedSub); errSN == nil {
					browseSubtypes = append(browseSubtypes, sn)
				}
			}
		}
		qKey := fmt.Sprintf("%s\x00%d\x00%d", strings.ToLower(qNameStr), q.Type, baseClass)
		if _, dup := seenQ[qKey]; !dup {
			seenQ[qKey] = struct{}{}
			matchedQuestions = append(matchedQuestions, dnsmessage.Question{
				Name:  q.Name,
				Type:  q.Type,
				Class: dnsmessage.Class(baseClass),
			})
		}
		if isQU {
			unicast = true
		}
	}

	if !answerTypeEnum && !answerBrowse && !answerLookup && len(browseSubtypes) == 0 {
		return nil, false
	}

	v4, v6 := splitIPv4AndIPv6(ips)
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:            hdr.ID,
		Response:      true,
		Authoritative: true,
	})
	builder.EnableCompression()
	if legacyUnicast && len(matchedQuestions) > 0 {
		if errQ := builder.StartQuestions(); errQ != nil {
			return nil, false
		}
		for _, mq := range matchedQuestions {
			if errAddQ := builder.Question(mq); errAddQ != nil {
				return nil, false
			}
		}
	}
	if errAns := builder.StartAnswers(); errAns != nil {
		return nil, false
	}

	if answerTypeEnum {
		if err := builder.PTRResource(dnsmessage.ResourceHeader{
			Name:  svcTypeName,
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
			TTL:   svcTTL,
		}, dnsmessage.PTRResource{PTR: svcName}); err != nil {
			return nil, false
		}
	}
	if answerBrowse {
		if err := builder.PTRResource(dnsmessage.ResourceHeader{
			Name:  svcName,
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
			TTL:   svcTTL,
		}, dnsmessage.PTRResource{PTR: instName}); err != nil {
			return nil, false
		}
	}
	for _, sn := range browseSubtypes {
		if err := builder.PTRResource(dnsmessage.ResourceHeader{
			Name:  sn,
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
			TTL:   svcTTL,
		}, dnsmessage.PTRResource{PTR: instName}); err != nil {
			return nil, false
		}
	}
	if answerLookup {
		if err := builder.SRVResource(dnsmessage.ResourceHeader{
			Name:  instName,
			Type:  dnsmessage.TypeSRV,
			Class: rrClass,
			TTL:   svcTTL,
		}, dnsmessage.SRVResource{Port: svc.port, Target: hostName}); err != nil {
			return nil, false
		}
		if err := builder.TXTResource(dnsmessage.ResourceHeader{
			Name:  instName,
			Type:  dnsmessage.TypeTXT,
			Class: rrClass,
			TTL:   svcTTL,
		}, dnsmessage.TXTResource{TXT: svc.text}); err != nil {
			return nil, false
		}
		if !answerBrowse {
			_ = builder.PTRResource(dnsmessage.ResourceHeader{
				Name:  svcName,
				Type:  dnsmessage.TypePTR,
				Class: dnsmessage.ClassINET,
				TTL:   svcTTL,
			}, dnsmessage.PTRResource{PTR: instName})
		}
		if err := appendHostAddrResources(&builder, hostName, rrClass, hostTTL, v4, v6); err != nil {
			return nil, false
		}
	} else if answerBrowse || len(browseSubtypes) > 0 {
		if errExt := builder.StartAdditionals(); errExt != nil {
			return nil, false
		}
		_ = builder.SRVResource(dnsmessage.ResourceHeader{
			Name:  instName,
			Type:  dnsmessage.TypeSRV,
			Class: dnsmessage.ClassINET,
			TTL:   svcTTL,
		}, dnsmessage.SRVResource{Port: svc.port, Target: hostName})
		_ = builder.TXTResource(dnsmessage.ResourceHeader{
			Name:  instName,
			Type:  dnsmessage.TypeTXT,
			Class: dnsmessage.ClassINET,
			TTL:   svcTTL,
		}, dnsmessage.TXTResource{TXT: svc.text})
		_ = appendHostAddrResources(&builder, hostName, dnsmessage.ClassINET, hostTTL, v4, v6)
	}

	out, errFinish := builder.Finish()
	if errFinish != nil {
		return nil, false
	}
	return rewriteWireLabelChar(out, dotInLabelSentinel, '.'), unicast
}

type hostRecordResponder struct {
	mu            sync.Mutex
	inner         zeroconfServer
	svc           mdnsServiceConfig
	baseHost      string
	osHost        string
	domain        string
	attempt       int
	hostFQDN      string
	fixedIPs      []string
	ifaces        []net.Interface
	rebindInner   func(string) (zeroconfServer, error)
	probeConflict hostConflictProbeFunc
	onSend        func(hostFQDN string, unicast bool, pkt []byte)
	inConflict    bool
	stopped       bool
	ipv4conn      *ipv4.PacketConn
	ipv6conn      *ipv6.PacketConn
	stopCh        chan struct{}
	wg            sync.WaitGroup
	shutdownOnce  sync.Once
}

func (r *hostRecordResponder) configureService(instance, service, domain string, port int, text []string) {
	r.mu.Lock()
	r.svc = parseMDNSServiceConfig(instance, service, domain, port, text)
	r.mu.Unlock()
}

func (r *hostRecordResponder) sendIfHostCurrent(expectedHostFQDN string, unicast bool, buf []byte, ifIndex int, from net.Addr) bool {
	if len(buf) == 0 || expectedHostFQDN == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.hostFQDN == "" || r.hostFQDN != expectedHostFQDN {
		return false
	}
	if r.onSend != nil {
		r.onSend(expectedHostFQDN, unicast, buf)
	}
	if unicast && from != nil {
		r.sendUnicast(buf, ifIndex, from)
		return true
	}
	r.sendMulticast(buf, ifIndex)
	return true
}

func (r *hostRecordResponder) announceServiceOnce() {
	r.mu.Lock()
	if r.stopped || r.hostFQDN == "" || r.svc.serviceName == "" {
		r.mu.Unlock()
		return
	}
	svc := r.svc
	hostFQDN := r.hostFQDN
	r.mu.Unlock()

	for _, iface := range r.ifaces {
		ips := r.ipsForInterface(iface.Index)
		if len(ips) == 0 {
			continue
		}
		if pkt := buildServicePacket(svc, hostFQDN, ips, mdnsServiceRecordTTL); len(pkt) > 0 {
			_ = r.sendIfHostCurrent(hostFQDN, false, pkt, iface.Index, nil)
		}
	}
}

func (r *hostRecordResponder) announceService() {
	r.scheduleServiceAnnouncements(mdnsAnnounceRepetitions, mdnsAnnounceInterval, r.announceServiceOnce)
}

func (r *hostRecordResponder) scheduleServiceAnnouncements(repetitions int, initialDelay time.Duration, sendOnce func()) {
	if repetitions < 1 || sendOnce == nil {
		return
	}
	r.mu.Lock()
	if r.stopped || r.hostFQDN == "" || r.svc.serviceName == "" {
		r.mu.Unlock()
		return
	}
	if repetitions > 1 {
		r.wg.Add(1)
	}
	r.mu.Unlock()

	sendOnce()
	if repetitions <= 1 {
		return
	}
	go func() {
		defer r.wg.Done()
		delay := initialDelay
		for i := 1; i < repetitions; i++ {
			timer := time.NewTimer(delay)
			select {
			case <-r.stopCh:
				timer.Stop()
				return
			case <-timer.C:
				sendOnce()
				delay *= 2
			}
		}
	}()
}

func newHostRecordResponder(inner zeroconfServer, host, domain string, fixedIPs []string, ifaces []net.Interface) (zeroconfServer, error) {
	return newHostRecordResponderWithRebind(inner, host, domain, fixedIPs, ifaces, nil)
}

func newHostRecordResponderWithRebind(inner zeroconfServer, host, domain string, fixedIPs []string, ifaces []net.Interface, rebind func(string) (zeroconfServer, error)) (zeroconfServer, error) {
	ipv4conn, err4 := openMDNSConn4(ifaces)
	ipv6conn, err6 := openMDNSConn6(ifaces)
	if ipv4conn == nil && ipv6conn == nil {
		if inner != nil {
			inner.Shutdown()
		}
		return nil, fmt.Errorf("discovery: failed to start host record responder (ipv4: %v, ipv6: %v)", err4, err6)
	}
	r := &hostRecordResponder{
		inner:         inner,
		baseHost:      host,
		osHost:        extractCleanHost(),
		domain:        domain,
		attempt:       1,
		hostFQDN:      formatHostFQDN(host, domain),
		fixedIPs:      append([]string(nil), fixedIPs...),
		ifaces:        append([]net.Interface(nil), ifaces...),
		rebindInner:   rebind,
		probeConflict: probeHostConflictOnLAN,
		ipv4conn:      ipv4conn,
		ipv6conn:      ipv6conn,
		stopCh:        make(chan struct{}),
	}
	if r.ipv4conn != nil {
		r.wg.Add(1)
		go r.recv4()
	}
	if r.ipv6conn != nil {
		r.wg.Add(1)
		go r.recv6()
	}
	return r, nil
}

func openMDNSConn4(ifaces []net.Interface) (*ipv4.PacketConn, error) {
	udpConn, err := net.ListenUDP("udp4", mdnsWildcardAddrIPv4)
	if err != nil {
		return nil, err
	}
	pkConn := ipv4.NewPacketConn(udpConn)
	_ = pkConn.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true)
	joined := 0
	var lastErr error
	for i := range ifaces {
		if errJoin := pkConn.JoinGroup(&ifaces[i], mdnsGroupAddrIPv4); errJoin == nil {
			joined++
		} else {
			lastErr = errJoin
		}
	}
	if joined == 0 {
		_ = pkConn.Close()
		if lastErr == nil {
			lastErr = fmt.Errorf("no multicast interfaces joined")
		}
		return nil, lastErr
	}
	_ = pkConn.SetMulticastTTL(255)
	return pkConn, nil
}

func openMDNSConn6(ifaces []net.Interface) (*ipv6.PacketConn, error) {
	udpConn, err := net.ListenUDP("udp6", mdnsWildcardAddrIPv6)
	if err != nil {
		return nil, err
	}
	pkConn := ipv6.NewPacketConn(udpConn)
	_ = pkConn.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true)
	joined := 0
	var lastErr error
	for i := range ifaces {
		if errJoin := pkConn.JoinGroup(&ifaces[i], mdnsGroupAddrIPv6); errJoin == nil {
			joined++
		} else {
			lastErr = errJoin
		}
	}
	if joined == 0 {
		_ = pkConn.Close()
		if lastErr == nil {
			lastErr = fmt.Errorf("no multicast interfaces joined")
		}
		return nil, lastErr
	}
	_ = pkConn.SetMulticastHopLimit(255)
	return pkConn, nil
}

func (r *hostRecordResponder) ifaceAllowed(ifIndex int) bool {
	if ifIndex == 0 {
		return true
	}
	for _, iface := range r.ifaces {
		if iface.Index == ifIndex {
			return true
		}
	}
	return false
}

func (r *hostRecordResponder) ipsForInterface(ifIndex int) []string {
	if len(r.fixedIPs) > 0 {
		return r.fixedIPs
	}
	if ifIndex != 0 {
		for _, iface := range r.ifaces {
			if iface.Index == ifIndex {
				addrs, err := iface.Addrs()
				if err != nil {
					return nil
				}
				return extractInterfaceAddrs(addrs)
			}
		}
		if iface, err := net.InterfaceByIndex(ifIndex); err == nil && iface != nil {
			if addrs, errAddrs := iface.Addrs(); errAddrs == nil {
				return extractInterfaceAddrs(addrs)
			}
		}
		return nil
	}
	return extractInterfaceIPs(r.ifaces)
}

func (r *hostRecordResponder) recv4() {
	defer r.wg.Done()
	buf := make([]byte, 65536)
	for {
		select {
		case <-r.stopCh:
			return
		default:
			n, cm, from, err := r.ipv4conn.ReadFrom(buf)
			if err != nil {
				continue
			}
			ifIndex := 0
			isMulticastDst := false
			if cm != nil {
				ifIndex = cm.IfIndex
				if len(cm.Dst) > 0 {
					isMulticastDst = cm.Dst.IsMulticast()
				}
			}
			r.handlePacketWithMeta(buf[:n], ifIndex, from, isMulticastDst)
		}
	}
}

func (r *hostRecordResponder) recv6() {
	defer r.wg.Done()
	buf := make([]byte, 65536)
	for {
		select {
		case <-r.stopCh:
			return
		default:
			n, cm, from, err := r.ipv6conn.ReadFrom(buf)
			if err != nil {
				continue
			}
			ifIndex := 0
			isMulticastDst := false
			if cm != nil {
				ifIndex = cm.IfIndex
				if len(cm.Dst) > 0 {
					isMulticastDst = cm.Dst.IsMulticast()
				}
			}
			r.handlePacketWithMeta(buf[:n], ifIndex, from, isMulticastDst)
		}
	}
}

func (r *hostRecordResponder) currentHostFQDN() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hostFQDN
}

func (r *hostRecordResponder) goodbyePacketsLocked(hostFQDN string) map[int][]byte {
	if hostFQDN == "" {
		return nil
	}
	if len(r.fixedIPs) > 0 && r.inner != nil {
		return nil
	}
	out := make(map[int][]byte, len(r.ifaces))
	for _, iface := range r.ifaces {
		ips := r.ipsForInterface(iface.Index)
		if len(ips) == 0 {
			continue
		}
		var pkt []byte
		if r.svc.serviceName != "" {
			pkt = buildServicePacket(r.svc, hostFQDN, ips, 0)
		} else {
			pkt = buildHostGoodbyePacket(hostFQDN, ips)
		}
		if len(pkt) > 0 {
			out[iface.Index] = pkt
		}
	}
	return out
}

func (r *hostRecordResponder) goodbyePacketsForHost(hostFQDN string) map[int][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.goodbyePacketsLocked(hostFQDN)
}

func (r *hostRecordResponder) sendGoodbyeLocked(hostFQDN string) {
	for ifIndex, pkt := range r.goodbyePacketsLocked(hostFQDN) {
		r.sendMulticast(pkt, ifIndex)
	}
}

func (r *hostRecordResponder) sendGoodbyeForHost(hostFQDN string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sendGoodbyeLocked(hostFQDN)
}

func (r *hostRecordResponder) withdrawConflictedHost(observedFQDN string) {
	r.mu.Lock()
	if r.stopped || r.hostFQDN == "" || r.hostFQDN != observedFQDN {
		r.mu.Unlock()
		return
	}
	oldFQDN := r.hostFQDN
	oldInner := r.inner
	r.hostFQDN = ""
	r.inner = nil
	r.sendGoodbyeLocked(oldFQDN)
	r.mu.Unlock()

	if oldInner != nil {
		oldInner.Shutdown()
	}
}

func (r *hostRecordResponder) handleConflict(observedFQDN string) {
	r.mu.Lock()
	if r.stopped || r.inConflict || r.hostFQDN == "" || r.hostFQDN != observedFQDN {
		r.mu.Unlock()
		return
	}
	if r.attempt >= maxHostCollisionAttempts {
		r.mu.Unlock()
		log.Warnf("discovery: conflict attempts exhausted for %s; withdrawing host advertisement", observedFQDN)
		r.withdrawConflictedHost(observedFQDN)
		return
	}
	r.inConflict = true
	startAttempt := r.attempt + 1
	baseHost := r.baseHost
	osHost := r.osHost
	domain := r.domain
	rebind := r.rebindInner
	probe := r.probeConflict
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.inConflict = false
		r.mu.Unlock()
	}()

	localIPs := r.allLocalIPs()
	nextHost, nextAttempt, errResolve := resolveAdvertisedHostCandidate(context.Background(), baseHost, startAttempt, osHost, domain, r.ifaces, localIPs, probe)
	if errResolve != nil {
		log.Warnf("discovery: unable to resolve replacement host after conflict on %s: %v; withdrawing host advertisement", observedFQDN, errResolve)
		r.withdrawConflictedHost(observedFQDN)
		return
	}

	var nextInner zeroconfServer
	if rebind != nil {
		var errRebind error
		nextInner, errRebind = rebind(nextHost)
		if errRebind != nil || nextInner == nil {
			log.Warnf("discovery: failed to rebind service under replacement host %s: %v; withdrawing host advertisement", nextHost, errRebind)
			r.withdrawConflictedHost(observedFQDN)
			return
		}
	}

	r.mu.Lock()
	if r.stopped || r.hostFQDN != observedFQDN {
		r.mu.Unlock()
		if nextInner != nil {
			nextInner.Shutdown()
		}
		return
	}
	oldFQDN := r.hostFQDN
	oldInner := r.inner
	r.attempt = nextAttempt
	r.hostFQDN = formatHostFQDN(nextHost, domain)
	if nextInner != nil {
		r.inner = nextInner
	}
	r.sendGoodbyeLocked(oldFQDN)
	r.mu.Unlock()

	if oldInner != nil && nextInner != nil && oldInner != nextInner {
		oldInner.Shutdown()
	}
	r.announceService()
}

func (r *hostRecordResponder) allLocalIPs() []string {
	if len(r.fixedIPs) > 0 {
		return r.fixedIPs
	}
	return extractInterfaceIPs(r.ifaces)
}

func hostAnswerHasForeignIP(answerIPs, localIPs []string) bool {
	if len(answerIPs) == 0 {
		return false
	}
	if len(localIPs) == 0 {
		return true
	}
	localSet := make(map[string]struct{}, len(localIPs))
	for _, raw := range localIPs {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
			localSet[ip.String()] = struct{}{}
		}
	}
	for _, raw := range answerIPs {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil {
			continue
		}
		if _, ok := localSet[ip.String()]; !ok {
			return true
		}
	}
	return false
}

func isOnLinkSource(from net.Addr, ifIndex int, ifaces []net.Interface, configuredIPs []string) bool {
	uaddr, ok := from.(*net.UDPAddr)
	if !ok || uaddr == nil || uaddr.IP == nil {
		return false
	}
	if uaddr.IP.IsLoopback() || uaddr.IP.IsLinkLocalUnicast() {
		return true
	}
	checkedKernelAddrs := false
	for _, iface := range ifaces {
		if ifIndex != 0 && iface.Index != ifIndex {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		checkedKernelAddrs = true
		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				if v.Contains(uaddr.IP) {
					return true
				}
			case *net.IPAddr:
				if v.IP.Equal(uaddr.IP) {
					return true
				}
			}
		}
	}
	if !checkedKernelAddrs {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil || len(addrs) == 0 {
				continue
			}
			checkedKernelAddrs = true
			for _, addr := range addrs {
				switch v := addr.(type) {
				case *net.IPNet:
					if v.Contains(uaddr.IP) {
						return true
					}
				case *net.IPAddr:
					if v.IP.Equal(uaddr.IP) {
						return true
					}
				}
			}
		}
	}
	for _, raw := range configuredIPs {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || isLocalInterfaceIP(ip, ifaces) {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			mask := net.CIDRMask(24, 32)
			subnet := net.IPNet{IP: ip4.Mask(mask), Mask: mask}
			if subnet.Contains(uaddr.IP) {
				return true
			}
			continue
		}
		if ip16 := ip.To16(); ip16 != nil {
			mask := net.CIDRMask(64, 128)
			subnet := net.IPNet{IP: ip16.Mask(mask), Mask: mask}
			if subnet.Contains(uaddr.IP) {
				return true
			}
		}
	}
	return false
}

func isValidMDNSResponseSource(from net.Addr, ifIndex int, ifaces []net.Interface, configuredIPs []string) bool {
	uaddr, ok := from.(*net.UDPAddr)
	if !ok || uaddr == nil || uaddr.Port != mdnsPort {
		return false
	}
	return isOnLinkSource(from, ifIndex, ifaces, configuredIPs)
}

func isValidMDNSResponsePeer(from net.Addr, ifIndex int, ifaces []net.Interface, configuredIPs []string, isMulticastDst bool) bool {
	uaddr, ok := from.(*net.UDPAddr)
	if !ok || uaddr == nil || uaddr.Port != mdnsPort || uaddr.IP == nil {
		return false
	}
	if isMulticastDst {
		return true
	}
	return isOnLinkSource(from, ifIndex, ifaces, configuredIPs)
}

func (r *hostRecordResponder) handlePacket(pkt []byte, ifIndex int, from net.Addr) {
	r.handlePacketWithMeta(pkt, ifIndex, from, false)
}

type mdnsQueryReply struct {
	payload []byte
	unicast bool
}

func buildCombinedQueryReplies(pkt []byte, svc mdnsServiceConfig, hostFQDN string, ips []string, legacyUnicast bool) []mdnsQueryReply {
	var out []mdnsQueryReply
	if legacyUnicast {
		if hostReply, hostUnicast := buildHostAddressReplyFiltered(pkt, hostFQDN, ips, true, nil); len(hostReply) > 0 {
			out = append(out, mdnsQueryReply{payload: hostReply, unicast: hostUnicast})
		}
		if svc.serviceName != "" {
			if svcReply, svcUnicast := buildServiceQueryReplyFiltered(pkt, svc, hostFQDN, ips, true, nil); len(svcReply) > 0 {
				out = append(out, mdnsQueryReply{payload: svcReply, unicast: svcUnicast})
			}
		}
		return out
	}
	for _, wantQU := range []bool{false, true} {
		qu := wantQU
		if hostReply, hostUnicast := buildHostAddressReplyFiltered(pkt, hostFQDN, ips, false, &qu); len(hostReply) > 0 {
			out = append(out, mdnsQueryReply{payload: hostReply, unicast: hostUnicast})
		}
		if svc.serviceName != "" {
			if svcReply, svcUnicast := buildServiceQueryReplyFiltered(pkt, svc, hostFQDN, ips, false, &qu); len(svcReply) > 0 {
				out = append(out, mdnsQueryReply{payload: svcReply, unicast: svcUnicast})
			}
		}
	}
	return out
}

func (r *hostRecordResponder) handlePacketWithMeta(pkt []byte, ifIndex int, from net.Addr, isMulticastDst bool) {
	if !r.ifaceAllowed(ifIndex) {
		return
	}
	r.mu.Lock()
	hostFQDN := r.hostFQDN
	svc := r.svc
	r.mu.Unlock()
	if hostFQDN == "" {
		return
	}
	ips := r.ipsForInterface(ifIndex)
	if len(ips) == 0 {
		return
	}
	allLocal := r.allLocalIPs()
	if !isMulticastDst && from != nil && !isOnLinkSource(from, ifIndex, r.ifaces, allLocal) {
		return
	}
	if answerIPs := extractHostAnswerIPs(pkt, hostFQDN); len(answerIPs) > 0 {
		if isValidMDNSResponsePeer(from, ifIndex, r.ifaces, allLocal, isMulticastDst) && hostAnswerHasForeignIP(answerIPs, allLocal) {
			r.handleConflict(hostFQDN)
		}
		return
	}
	if hasHostResponseRecord(pkt, hostFQDN) {
		if isValidMDNSResponsePeer(from, ifIndex, r.ifaces, allLocal, isMulticastDst) {
			r.handleConflict(hostFQDN)
		}
		return
	}
	legacyUnicast := false
	if uaddr, ok := from.(*net.UDPAddr); ok && uaddr != nil && uaddr.Port != mdnsPort {
		legacyUnicast = true
	}
	for _, rep := range buildCombinedQueryReplies(pkt, svc, hostFQDN, ips, legacyUnicast) {
		_ = r.sendIfHostCurrent(hostFQDN, rep.unicast && from != nil, rep.payload, ifIndex, from)
	}
}

func (r *hostRecordResponder) sendUnicast(buf []byte, ifIndex int, from net.Addr) {
	uaddr, ok := from.(*net.UDPAddr)
	if !ok || uaddr == nil {
		return
	}
	if uaddr.IP.To4() != nil {
		if r.ipv4conn == nil {
			return
		}
		if ifIndex != 0 && supportsPerInterfaceAddrs(runtime.GOOS) {
			wcm := ipv4.ControlMessage{IfIndex: ifIndex}
			_, _ = r.ipv4conn.WriteTo(buf, &wcm, uaddr)
			return
		}
		_, _ = r.ipv4conn.WriteTo(buf, nil, uaddr)
		return
	}
	if r.ipv6conn == nil {
		return
	}
	if ifIndex != 0 && supportsPerInterfaceAddrs(runtime.GOOS) {
		wcm := ipv6.ControlMessage{IfIndex: ifIndex}
		_, _ = r.ipv6conn.WriteTo(buf, &wcm, uaddr)
		return
	}
	_, _ = r.ipv6conn.WriteTo(buf, nil, uaddr)
}

func (r *hostRecordResponder) sendMulticast(buf []byte, ifIndex int) {
	if r.ipv4conn != nil {
		if ifIndex != 0 {
			r.writeMulticast4(buf, ifIndex)
		} else {
			for _, iface := range r.ifaces {
				r.writeMulticast4(buf, iface.Index)
			}
		}
	}
	if r.ipv6conn != nil {
		if ifIndex != 0 {
			r.writeMulticast6(buf, ifIndex)
		} else {
			for _, iface := range r.ifaces {
				r.writeMulticast6(buf, iface.Index)
			}
		}
	}
}

func (r *hostRecordResponder) writeMulticast4(buf []byte, ifIndex int) {
	if supportsPerInterfaceAddrs(runtime.GOOS) {
		wcm := ipv4.ControlMessage{IfIndex: ifIndex}
		_, _ = r.ipv4conn.WriteTo(buf, &wcm, mdnsGroupAddrIPv4)
		return
	}
	if iface, err := net.InterfaceByIndex(ifIndex); err == nil && iface != nil {
		_ = r.ipv4conn.SetMulticastInterface(iface)
	}
	_, _ = r.ipv4conn.WriteTo(buf, nil, mdnsGroupAddrIPv4)
}

func (r *hostRecordResponder) writeMulticast6(buf []byte, ifIndex int) {
	if supportsPerInterfaceAddrs(runtime.GOOS) {
		wcm := ipv6.ControlMessage{IfIndex: ifIndex}
		_, _ = r.ipv6conn.WriteTo(buf, &wcm, mdnsGroupAddrIPv6)
		return
	}
	if iface, err := net.InterfaceByIndex(ifIndex); err == nil && iface != nil {
		_ = r.ipv6conn.SetMulticastInterface(iface)
	}
	_, _ = r.ipv6conn.WriteTo(buf, nil, mdnsGroupAddrIPv6)
}

func (r *hostRecordResponder) sendGoodbye() {
	r.sendGoodbyeForHost(r.currentHostFQDN())
}

func (r *hostRecordResponder) Shutdown() {
	r.shutdownOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		inner := r.inner
		r.inner = nil
		r.mu.Unlock()

		r.sendGoodbye()
		close(r.stopCh)
		if r.ipv4conn != nil {
			_ = r.ipv4conn.Close()
		}
		if r.ipv6conn != nil {
			_ = r.ipv6conn.Close()
		}
		r.wg.Wait()
		if inner != nil {
			inner.Shutdown()
		}
	})
}

// ZeroconfAdvertiser wraps libp2p/zeroconf/v2 Server with defensive lifecycle management.
type ZeroconfAdvertiser struct {
	mu                sync.Mutex
	server            zeroconfServer
	inFlight          bool
	closed            bool
	registerProxy     registerProxyFunc
	interfaceIPs      func([]net.Interface) []string
	osHostname        func() (string, error)
	probeHostConflict hostConflictProbeFunc
}

// NewZeroconfAdvertiser returns a new ZeroconfAdvertiser.
func NewZeroconfAdvertiser() *ZeroconfAdvertiser {
	return &ZeroconfAdvertiser{}
}

func cleanHostname(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimSuffix(h, ".")
	if len(h) >= 6 && strings.EqualFold(h[len(h)-6:], ".local") {
		h = h[:len(h)-6]
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "localhost"
	}
	return h
}

// extractCleanHost returns bare hostname without any .local suffix
// to prevent libp2p/zeroconf/v2 from appending a duplicate .local. (e.g. on macOS).
func extractCleanHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	return cleanHostname(h)
}

func sanitizeDNSLabel(s string) string {
	var b strings.Builder
	prevHyphen := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteByte(c)
			prevHyphen = false
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
			prevHyphen = false
		default:
			if b.Len() > 0 && !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// buildAdvertisedHost derives a proxy-specific RFC 1123 host label from the
// instance name so mDNS advertisement never claims or flushes the OS-owned
// <hostname>.local. record.
func buildAdvertisedHost(instanceName, osHost string) string {
	clean := sanitizeDNSLabel(instanceName)
	var rest string
	switch {
	case clean == "cpa":
		rest = "0001"
	case strings.HasPrefix(clean, "cpa-"):
		rest = strings.TrimPrefix(clean, "cpa-")
	default:
		rest = clean
	}
	rest = strings.Trim(rest, "-")
	if rest == "" {
		rest = "0001"
	}

	const prefix = "cpa-"
	maxRest := maxDNSLabelBytes - len(prefix)
	if len(rest) > maxRest {
		if len(rest) > 5 && rest[len(rest)-5] == '-' && isValidHex4(rest[len(rest)-4:]) {
			suffix := rest[len(rest)-5:]
			head := strings.TrimRight(rest[:maxRest-len(suffix)], "-")
			if head == "" {
				rest = strings.TrimPrefix(suffix, "-")
			} else {
				rest = head + suffix
			}
		} else {
			rest = strings.TrimRight(rest[:maxRest], "-")
		}
		if rest == "" {
			rest = "0001"
		}
	}

	label := prefix + rest
	cleanOS := strings.ToLower(cleanHostname(osHost))
	if cleanOS != "" && cleanOS != "localhost" && strings.EqualFold(label, cleanOS) {
		const altSuffix = "-svc"
		if len(label)+len(altSuffix) <= maxDNSLabelBytes {
			label += altSuffix
		} else {
			trimmed := strings.TrimRight(label[:maxDNSLabelBytes-len(altSuffix)], "-")
			if trimmed == "" {
				label = "cpa-0001" + altSuffix
			} else {
				label = trimmed + altSuffix
			}
		}
	}
	return label
}

func appendHostCollisionSuffix(baseLabel string, attempt int) string {
	if attempt <= 1 {
		return baseLabel
	}
	suffix := fmt.Sprintf("-%d", attempt)
	if len(baseLabel)+len(suffix) <= maxDNSLabelBytes {
		return baseLabel + suffix
	}
	trimmed := strings.TrimRight(baseLabel[:maxDNSLabelBytes-len(suffix)], "-")
	if trimmed == "" {
		return "cpa" + suffix
	}
	return trimmed + suffix
}

func extractHostAnswerIPs(pkt []byte, hostFQDN string) []string {
	normPkt := rewriteWireLabelChar(pkt, '.', dotInLabelSentinel)
	var p dnsmessage.Parser
	hdr, err := p.Start(normPkt)
	if err != nil || !hdr.Response {
		return nil
	}
	if errSkipQ := p.SkipAllQuestions(); errSkipQ != nil {
		return nil
	}
	var ips []string
	collect := func(nextHeader func() (dnsmessage.ResourceHeader, error), skip func() error) bool {
		for {
			rh, errH := nextHeader()
			if errH != nil {
				return errH == dnsmessage.ErrSectionDone
			}
			if !strings.EqualFold(rh.Name.String(), hostFQDN) || rh.TTL == 0 {
				if errSkip := skip(); errSkip != nil {
					return false
				}
				continue
			}
			switch rh.Type {
			case dnsmessage.TypeA:
				r, errA := p.AResource()
				if errA != nil {
					return false
				}
				ips = append(ips, net.IP(r.A[:]).String())
			case dnsmessage.TypeAAAA:
				r, errAAAA := p.AAAAResource()
				if errAAAA != nil {
					return false
				}
				ips = append(ips, net.IP(r.AAAA[:]).String())
			default:
				if errSkip := skip(); errSkip != nil {
					return false
				}
			}
		}
	}
	if !collect(p.AnswerHeader, p.SkipAnswer) {
		return ips
	}
	if errSkipAuth := p.SkipAllAuthorities(); errSkipAuth != nil {
		return ips
	}
	_ = collect(p.AdditionalHeader, p.SkipAdditional)
	return ips
}

func hasHostResponseRecord(pkt []byte, hostFQDN string) bool {
	normPkt := rewriteWireLabelChar(pkt, '.', dotInLabelSentinel)
	var p dnsmessage.Parser
	hdr, err := p.Start(normPkt)
	if err != nil || !hdr.Response {
		return false
	}
	if errSkipQ := p.SkipAllQuestions(); errSkipQ != nil {
		return false
	}
	found := false
	scan := func(nextHeader func() (dnsmessage.ResourceHeader, error), skip func() error) bool {
		for {
			rh, errH := nextHeader()
			if errH != nil {
				return errH == dnsmessage.ErrSectionDone
			}
			if errSkip := skip(); errSkip != nil {
				return false
			}
			if rh.TTL > 0 && strings.EqualFold(rh.Name.String(), hostFQDN) {
				found = true
				return false
			}
		}
	}
	if !scan(p.AnswerHeader, p.SkipAnswer) || found {
		return found
	}
	if errSkipAuth := p.SkipAllAuthorities(); errSkipAuth != nil {
		return false
	}
	_ = scan(p.AdditionalHeader, p.SkipAdditional)
	return found
}

func extractHostAuthorityIPs(pkt []byte, hostFQDN string) (uint16, []string, bool) {
	normPkt := rewriteWireLabelChar(pkt, '.', dotInLabelSentinel)
	var p dnsmessage.Parser
	hdr, err := p.Start(normPkt)
	if err != nil || hdr.Response {
		return 0, nil, false
	}
	qs, errQ := p.AllQuestions()
	if errQ != nil || len(qs) == 0 {
		return 0, nil, false
	}
	matchedQ := false
	for _, q := range qs {
		if strings.EqualFold(q.Name.String(), hostFQDN) {
			matchedQ = true
			break
		}
	}
	if !matchedQ {
		return 0, nil, false
	}
	if errSkipAns := p.SkipAllAnswers(); errSkipAns != nil {
		return 0, nil, false
	}
	var ips []string
	hasOtherType := false
	for {
		rh, errH := p.AuthorityHeader()
		if errH != nil {
			break
		}
		if !strings.EqualFold(rh.Name.String(), hostFQDN) || rh.TTL == 0 {
			if errSkip := p.SkipAuthority(); errSkip != nil {
				break
			}
			continue
		}
		switch rh.Type {
		case dnsmessage.TypeA:
			r, errA := p.AResource()
			if errA != nil {
				return hdr.ID, ips, hasOtherType
			}
			ips = append(ips, net.IP(r.A[:]).String())
		case dnsmessage.TypeAAAA:
			r, errAAAA := p.AAAAResource()
			if errAAAA != nil {
				return hdr.ID, ips, hasOtherType
			}
			ips = append(ips, net.IP(r.AAAA[:]).String())
		default:
			if errSkip := p.SkipAuthority(); errSkip != nil {
				return hdr.ID, ips, hasOtherType
			}
			hasOtherType = true
		}
	}
	return hdr.ID, ips, hasOtherType
}

func canonicalHostRecordList(ips []string) [][]byte {
	v4, v6 := splitIPv4AndIPv6(ips)
	records := make([][]byte, 0, len(v4)+len(v6))
	for _, addr := range v4 {
		rec := append([]byte{0, byte(dnsmessage.TypeA)}, addr[:]...)
		records = append(records, rec)
	}
	for _, addr := range v6 {
		rec := append([]byte{0, byte(dnsmessage.TypeAAAA)}, addr[:]...)
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return bytes.Compare(records[i], records[j]) < 0
	})
	dedup := records[:0]
	for _, rec := range records {
		if len(dedup) == 0 || !bytes.Equal(dedup[len(dedup)-1], rec) {
			dedup = append(dedup, rec)
		}
	}
	return dedup
}

// compareHostRecordSets compares two proposed record sets lexicographically per RFC 6762 §8.2.
// Returns -1 if localIPs < peerIPs (local loses), +1 if localIPs > peerIPs (local wins), 0 if equal.
func compareHostRecordSets(localIPs, peerIPs []string) int {
	ours := canonicalHostRecordList(localIPs)
	theirs := canonicalHostRecordList(peerIPs)
	limit := min(len(ours), len(theirs))
	for i := 0; i < limit; i++ {
		if cmp := bytes.Compare(ours[i], theirs[i]); cmp != 0 {
			return cmp
		}
	}
	switch {
	case len(ours) < len(theirs):
		return -1
	case len(ours) > len(theirs):
		return 1
	default:
		return 0
	}
}

func isLocalInterfaceIP(target net.IP, ifaces []net.Interface) bool {
	if target == nil {
		return false
	}
	for _, iface := range ifaces {
		if interfaceHasIP(iface, target) {
			return true
		}
	}
	return false
}

func isOwnLoopbackProbe(pkt, ownQuery []byte, from net.Addr, ownPorts map[int]struct{}, ifaces []net.Interface) bool {
	if len(ownQuery) == 0 || !bytes.Equal(pkt, ownQuery) {
		return false
	}
	uaddr, ok := from.(*net.UDPAddr)
	if !ok || uaddr == nil {
		return false
	}
	if _, portMatch := ownPorts[uaddr.Port]; !portMatch {
		return false
	}
	return isLocalInterfaceIP(uaddr.IP, ifaces)
}

func isHostProbeConflict(pkt []byte, hostFQDN string, localIPs []string, ownPort, peerPort int) bool {
	if hasHostResponseRecord(pkt, hostFQDN) {
		return true
	}
	theirID, authIPs, hasOtherType := extractHostAuthorityIPs(pkt, hostFQDN)
	if hasOtherType {
		return true
	}
	if len(authIPs) == 0 {
		return false
	}
	cmp := compareHostRecordSets(localIPs, authIPs)
	if cmp < 0 {
		return true
	}
	if cmp > 0 {
		return false
	}
	if ownPort > 0 && peerPort > 0 && ownPort != peerPort {
		return ownPort < peerPort
	}
	return theirID != 0
}

func ifaceInList(ifIndex int, ifaces []net.Interface) bool {
	if ifIndex <= 0 {
		return false
	}
	for _, iface := range ifaces {
		if iface.Index == ifIndex {
			return true
		}
	}
	return false
}

func isValidProbePeer(from net.Addr, ifIndex int, ifaces []net.Interface, configuredIPs []string, isMulticast, isResponse bool) bool {
	uaddr, ok := from.(*net.UDPAddr)
	if !ok || uaddr == nil || uaddr.IP == nil {
		return false
	}
	if isResponse && uaddr.Port != mdnsPort {
		return false
	}
	if isMulticast || ifaceInList(ifIndex, ifaces) {
		return true
	}
	return isOnLinkSource(from, ifIndex, ifaces, configuredIPs)
}

func buildHostProbeQuery(hostFQDN string, probeID uint16, localIPs []string) ([]byte, error) {
	qName, errName := dnsmessage.NewName(hostFQDN)
	if errName != nil {
		return nil, errName
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: probeID})
	builder.EnableCompression()
	if errStartQ := builder.StartQuestions(); errStartQ != nil {
		return nil, errStartQ
	}
	if errQ := builder.Question(dnsmessage.Question{
		Name:  qName,
		Type:  dnsmessage.TypeALL,
		Class: dnsmessage.ClassINET,
	}); errQ != nil {
		return nil, errQ
	}
	v4, v6 := splitIPv4AndIPv6(localIPs)
	if len(v4) > 0 || len(v6) > 0 {
		if errAuth := builder.StartAuthorities(); errAuth != nil {
			return nil, errAuth
		}
		for _, addr := range v4 {
			if errAdd := builder.AResource(dnsmessage.ResourceHeader{
				Name:  qName,
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsHostRecordTTL,
			}, dnsmessage.AResource{A: addr}); errAdd != nil {
				return nil, errAdd
			}
		}
		for _, addr := range v6 {
			if errAdd := builder.AAAAResource(dnsmessage.ResourceHeader{
				Name:  qName,
				Type:  dnsmessage.TypeAAAA,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsHostRecordTTL,
			}, dnsmessage.AAAAResource{AAAA: addr}); errAdd != nil {
				return nil, errAdd
			}
		}
	}
	return builder.Finish()
}

func randomProbeID() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err == nil {
		if id := binary.BigEndian.Uint16(b[:]); id != 0 {
			return id
		}
	}
	return uint16(os.Getpid()&0x7FFF) | 1
}

func awaitProbeConflictResult(conflictCh <-chan bool, doneCh <-chan struct{}, detected *atomic.Bool, ctx context.Context, stopReaders func()) (bool, error) {
	select {
	case <-conflictCh:
		if stopReaders != nil {
			stopReaders()
		}
		<-doneCh
		return true, nil
	case <-doneCh:
		if detected != nil && detected.Load() {
			return true, nil
		}
		select {
		case <-conflictCh:
			return true, nil
		default:
		}
		if ctx != nil && ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
}

func runHostProbeSchedule(ctx context.Context, probeCount int, probeInterval time.Duration, sendProbe func() error, conflictCh <-chan bool, doneCh <-chan struct{}, detected *atomic.Bool, stopReaders func()) (bool, error) {
	if probeCount < 1 {
		probeCount = 1
	}
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	for i := 0; i < probeCount; i++ {
		if ctx != nil && ctx.Err() != nil {
			if stopReaders != nil {
				stopReaders()
			}
			<-doneCh
			if detected != nil && detected.Load() {
				return true, nil
			}
			return false, ctx.Err()
		}
		if errSend := sendProbe(); errSend != nil {
			if stopReaders != nil {
				stopReaders()
			}
			<-doneCh
			return false, errSend
		}
		timer := time.NewTimer(probeInterval)
		select {
		case <-conflictCh:
			timer.Stop()
			if stopReaders != nil {
				stopReaders()
			}
			<-doneCh
			return true, nil
		case <-ctxDone:
			timer.Stop()
			if stopReaders != nil {
				stopReaders()
			}
			<-doneCh
			if detected != nil && detected.Load() {
				return true, nil
			}
			return false, ctx.Err()
		case <-doneCh:
			timer.Stop()
			if detected != nil && detected.Load() {
				return true, nil
			}
			select {
			case <-conflictCh:
				return true, nil
			default:
			}
			if ctx != nil && ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, fmt.Errorf("discovery: host conflict probe readers terminated before probe window completed")
		case <-timer.C:
			if detected != nil && detected.Load() {
				if stopReaders != nil {
					stopReaders()
				}
				<-doneCh
				return true, nil
			}
		}
	}
	if stopReaders != nil {
		stopReaders()
	}
	return awaitProbeConflictResult(conflictCh, doneCh, detected, ctx, stopReaders)
}

func probeHostConflictOnLAN(ctx context.Context, hostFQDN string, ifaces []net.Interface, localIPs []string) (bool, error) {
	if len(ifaces) == 0 {
		return false, fmt.Errorf("discovery: cannot probe host conflict with empty interface list")
	}
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return false, errCtx
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < time.Duration(mdnsHostProbeCount)*mdnsHostProbeInterval {
			return false, context.DeadlineExceeded
		}
	}
	probeID := randomProbeID()
	queryBytes, errQuery := buildHostProbeQuery(hostFQDN, probeID, localIPs)
	if errQuery != nil {
		return false, errQuery
	}

	type probeConn struct {
		conn           *net.UDPConn
		pconn4         *ipv4.PacketConn
		pconn6         *ipv6.PacketConn
		isMulticastDst bool
	}
	var conns []probeConn
	defer func() {
		for _, c := range conns {
			_ = c.conn.Close()
		}
	}()

	ownPorts := make(map[int]struct{}, 2)
	ownPort4 := 0
	ownPort6 := 0
	if mConn4, errM4 := net.ListenUDP("udp4", mdnsWildcardAddrIPv4); errM4 == nil {
		mp4 := ipv4.NewPacketConn(mConn4)
		_ = mp4.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true)
		joined := 0
		for i := range ifaces {
			if mp4.JoinGroup(&ifaces[i], mdnsGroupAddrIPv4) == nil {
				joined++
			}
		}
		if joined > 0 {
			_ = mp4.SetMulticastTTL(255)
			conns = append(conns, probeConn{conn: mConn4, pconn4: mp4, isMulticastDst: true})
		} else {
			_ = mConn4.Close()
		}
	}
	if mConn6, errM6 := net.ListenUDP("udp6", mdnsWildcardAddrIPv6); errM6 == nil {
		mp6 := ipv6.NewPacketConn(mConn6)
		_ = mp6.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true)
		joined := 0
		for i := range ifaces {
			if mp6.JoinGroup(&ifaces[i], mdnsGroupAddrIPv6) == nil {
				joined++
			}
		}
		if joined > 0 {
			_ = mp6.SetMulticastHopLimit(255)
			conns = append(conns, probeConn{conn: mConn6, pconn6: mp6, isMulticastDst: true})
		} else {
			_ = mConn6.Close()
		}
	}

	var pConn4 *ipv4.PacketConn
	var pConn6 *ipv6.PacketConn
	var lastInitErr error
	if uConn4, errListen4 := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0}); errListen4 != nil {
		lastInitErr = errListen4
	} else {
		pConn4 = ipv4.NewPacketConn(uConn4)
		_ = pConn4.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true)
		conns = append(conns, probeConn{conn: uConn4, pconn4: pConn4, isMulticastDst: false})
		if laddr, ok := uConn4.LocalAddr().(*net.UDPAddr); ok && laddr != nil {
			ownPorts[laddr.Port] = struct{}{}
			ownPort4 = laddr.Port
		}
	}
	if uConn6, errListen6 := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6zero, Port: 0}); errListen6 != nil {
		lastInitErr = errListen6
	} else {
		pConn6 = ipv6.NewPacketConn(uConn6)
		_ = pConn6.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true)
		conns = append(conns, probeConn{conn: uConn6, pconn6: pConn6, isMulticastDst: false})
		if laddr, ok := uConn6.LocalAddr().(*net.UDPAddr); ok && laddr != nil {
			ownPorts[laddr.Port] = struct{}{}
			ownPort6 = laddr.Port
		}
	}
	if pConn4 == nil && pConn6 == nil {
		if lastInitErr == nil {
			lastInitErr = fmt.Errorf("no probe sockets available")
		}
		return false, fmt.Errorf("discovery: failed to initialize mDNS host conflict probe for %s: %w", hostFQDN, lastInitErr)
	}

	sendOnce := func() error {
		var lastErr error
		sentPerIface := make([]int, len(ifaces))
		if pConn4 != nil {
			for i := range ifaces {
				if errSet := pConn4.SetMulticastInterface(&ifaces[i]); errSet != nil {
					lastErr = errSet
					continue
				}
				if _, errWrite := pConn4.WriteTo(queryBytes, nil, mdnsGroupAddrIPv4); errWrite == nil {
					sentPerIface[i]++
				} else {
					lastErr = errWrite
				}
			}
		}
		if pConn6 != nil {
			for i := range ifaces {
				if errSet := pConn6.SetMulticastInterface(&ifaces[i]); errSet != nil {
					lastErr = errSet
					continue
				}
				if _, errWrite := pConn6.WriteTo(queryBytes, nil, mdnsGroupAddrIPv6); errWrite == nil {
					sentPerIface[i]++
				} else {
					lastErr = errWrite
				}
			}
		}
		for i := range ifaces {
			if sentPerIface[i] == 0 {
				if lastErr == nil {
					lastErr = fmt.Errorf("no probe packets sent")
				}
				return fmt.Errorf("discovery: failed to send mDNS host conflict probe for %s on interface %s: %w", hostFQDN, ifaces[i].Name, lastErr)
			}
		}
		return nil
	}

	var detected atomic.Bool
	conflictCh := make(chan bool, len(conns))
	var wg sync.WaitGroup
	for _, pc := range conns {
		wg.Add(1)
		go func(entry probeConn) {
			defer wg.Done()
			buf := make([]byte, 4096)
			for {
				if ctx != nil && ctx.Err() != nil {
					return
				}
				var (
					n        int
					ifIndex  int
					fromAddr net.Addr
					errRead  error
					isMcast  bool
				)
				switch {
				case entry.pconn4 != nil:
					var cm *ipv4.ControlMessage
					n, cm, fromAddr, errRead = entry.pconn4.ReadFrom(buf)
					if cm != nil {
						ifIndex = cm.IfIndex
						if len(cm.Dst) > 0 {
							isMcast = cm.Dst.IsMulticast()
						}
					}
				case entry.pconn6 != nil:
					var cm *ipv6.ControlMessage
					n, cm, fromAddr, errRead = entry.pconn6.ReadFrom(buf)
					if cm != nil {
						ifIndex = cm.IfIndex
						if len(cm.Dst) > 0 {
							isMcast = cm.Dst.IsMulticast()
						}
					}
				default:
					n, fromAddr, errRead = entry.conn.ReadFromUDP(buf)
				}
				if errRead != nil {
					return
				}
				pkt := buf[:n]
				if isOwnLoopbackProbe(pkt, queryBytes, fromAddr, ownPorts, ifaces) {
					continue
				}
				isResp := hasHostResponseRecord(pkt, hostFQDN)
				if !isValidProbePeer(fromAddr, ifIndex, ifaces, localIPs, isMcast, isResp) {
					continue
				}
				peerPort := 0
				ownPort := ownPort4
				if uaddr, ok := fromAddr.(*net.UDPAddr); ok && uaddr != nil {
					peerPort = uaddr.Port
					if uaddr.IP != nil && uaddr.IP.To4() == nil && ownPort6 != 0 {
						ownPort = ownPort6
					}
				}
				if isHostProbeConflict(pkt, hostFQDN, localIPs, ownPort, peerPort) {
					detected.Store(true)
					conflictCh <- true
					return
				}
			}
		}(pc)
	}

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	var stopOnce sync.Once
	stopReaders := func() {
		stopOnce.Do(func() {
			for _, c := range conns {
				_ = c.conn.Close()
			}
		})
	}
	return runHostProbeSchedule(ctx, mdnsHostProbeCount, mdnsHostProbeInterval, sendOnce, conflictCh, doneCh, &detected, stopReaders)
}

func resolveAdvertisedHostCandidate(ctx context.Context, baseHost string, startAttempt int, osHost, domain string, ifaces []net.Interface, localIPs []string, probeConflict hostConflictProbeFunc) (string, int, error) {
	cleanOS := strings.ToLower(cleanHostname(osHost))
	if probeConflict == nil {
		probeConflict = probeHostConflictOnLAN
	}
	if startAttempt < 1 {
		startAttempt = 1
	}
	for attempt := startAttempt; attempt <= maxHostCollisionAttempts; attempt++ {
		if ctx != nil && ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		candidate := appendHostCollisionSuffix(baseHost, attempt)
		if cleanOS != "" && cleanOS != "localhost" && strings.EqualFold(candidate, cleanOS) {
			continue
		}
		conflict, errProbe := probeConflict(ctx, formatHostFQDN(candidate, domain), ifaces, localIPs)
		if errProbe != nil {
			return "", 0, errProbe
		}
		if !conflict {
			return candidate, attempt, nil
		}
	}
	return "", 0, fmt.Errorf("discovery: unable to resolve non-conflicting mDNS host label for %q after %d attempts", baseHost, maxHostCollisionAttempts)
}

func resolveAdvertisedHost(ctx context.Context, instanceName, osHost, domain string, ifaces []net.Interface, localIPs []string, probeConflict hostConflictProbeFunc) (string, error) {
	baseHost := buildAdvertisedHost(instanceName, osHost)
	candidate, _, err := resolveAdvertisedHostCandidate(ctx, baseHost, 1, osHost, domain, ifaces, localIPs, probeConflict)
	return candidate, err
}

func extractInterfaceAddrs(addrs []net.Addr) []string {
	var v4, v6Global, v6LinkLocal []string
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			v4 = append(v4, ip4.String())
			continue
		}
		if ip16 := ip.To16(); ip16 != nil {
			if ip16.IsGlobalUnicast() {
				v6Global = append(v6Global, ip16.String())
			} else if ip16.IsLinkLocalUnicast() {
				v6LinkLocal = append(v6LinkLocal, ip16.String())
			}
		}
	}
	if len(v6Global) > 0 {
		return append(v4, v6Global...)
	}
	return append(v4, v6LinkLocal...)
}

// extractInterfaceIPs collects non-loopback IP addresses from the selected interfaces.
func extractInterfaceIPs(ifaces []net.Interface) []string {
	var ips []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		ips = append(ips, extractInterfaceAddrs(addrs)...)
	}
	return ips
}

func supportsPerInterfaceAddrs(goos string) bool {
	switch goos {
	case "darwin", "ios", "linux":
		return true
	default:
		return false
	}
}

func sameIPSet(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	setA := make(map[string]struct{}, len(a))
	for _, raw := range a {
		trimmed := strings.TrimSpace(raw)
		if ip := net.ParseIP(trimmed); ip != nil {
			setA[ip.String()] = struct{}{}
		} else if trimmed != "" {
			setA[trimmed] = struct{}{}
		}
	}
	setB := make(map[string]struct{}, len(b))
	for _, raw := range b {
		trimmed := strings.TrimSpace(raw)
		if ip := net.ParseIP(trimmed); ip != nil {
			setB[ip.String()] = struct{}{}
		} else if trimmed != "" {
			setB[trimmed] = struct{}{}
		}
	}
	if len(setA) != len(setB) {
		return false
	}
	for k := range setA {
		if _, ok := setB[k]; !ok {
			return false
		}
	}
	return true
}

func resolveRegisterProxyIPs(ifaces []net.Interface, advertisedIPs []string, goos string, lookupIPs func([]net.Interface) []string) ([]string, error) {
	if lookupIPs == nil {
		lookupIPs = extractInterfaceIPs
	}
	interfaceIPs := lookupIPs(ifaces)
	ips := advertisedIPs
	if len(ips) == 0 {
		ips = interfaceIPs
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("discovery: no usable IP addresses found on specified interfaces")
	}
	if supportsPerInterfaceAddrs(goos) && len(interfaceIPs) > 0 && (len(advertisedIPs) == 0 || (len(ifaces) > 1 && sameIPSet(advertisedIPs, interfaceIPs))) {
		return nil, nil
	}
	return ips, nil
}

// Start registers and starts mDNS advertisement for the primary service type
// and any configured API protocol subtypes.
func (a *ZeroconfAdvertiser) Start(ctx context.Context, spec ServiceSpec) (err error) {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if len(spec.Interfaces) == 0 {
		return fmt.Errorf("discovery: cannot start advertiser with empty interface list (refusing fallback to all interfaces)")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("discovery: invalid service port %d (must be between 1 and 65535)", spec.Port)
	}

	a.mu.Lock()
	if a.server != nil || a.inFlight {
		a.mu.Unlock()
		return fmt.Errorf("discovery: advertiser already started")
	}
	a.inFlight = true
	a.closed = false
	a.mu.Unlock()

	var server zeroconfServer
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("discovery: panic starting advertiser: %v", r)
			log.Errorf("%v", err)
		}
		a.mu.Lock()
		a.inFlight = false
		if err != nil {
			hold := a.server
			a.server = nil
			a.mu.Unlock()
			if hold != nil {
				hold.Shutdown()
			}
			if server != nil && hold != server {
				server.Shutdown()
			}
			return
		}
		a.mu.Unlock()
	}()

	domain := spec.Domain
	if domain == "" {
		domain = DefaultDomain
	}
	serviceType := spec.ServiceType
	if serviceType == "" {
		serviceType = DefaultServiceType
	}

	proxyIPs, errIPs := resolveRegisterProxyIPs(spec.Interfaces, spec.AdvertisedIPs, runtime.GOOS, a.interfaceIPs)
	if errIPs != nil {
		return errIPs
	}

	spec.InstanceName = sanitizeInstanceName(spec.InstanceName)
	if spec.InstanceName == "" {
		spec.InstanceName = DefaultInstancePrefix + "0001"
	}
	cleanHost := extractCleanHost()
	if a.osHostname != nil {
		if rawHost, errHost := a.osHostname(); errHost == nil && rawHost != "" {
			cleanHost = cleanHostname(rawHost)
		} else {
			cleanHost = "localhost"
		}
	}
	localIPs := spec.AdvertisedIPs
	if len(localIPs) == 0 {
		if a.interfaceIPs != nil {
			localIPs = a.interfaceIPs(spec.Interfaces)
		} else {
			localIPs = extractInterfaceIPs(spec.Interfaces)
		}
	}
	probeConflict := a.probeHostConflict
	if probeConflict == nil && a.registerProxy != nil {
		probeConflict = func(context.Context, string, []net.Interface, []string) (bool, error) { return false, nil }
	}
	baseHost := buildAdvertisedHost(spec.InstanceName, cleanHost)
	advertisedHost, initialAttempt, errHost := resolveAdvertisedHostCandidate(ctx, baseHost, 1, cleanHost, domain, spec.Interfaces, localIPs, probeConflict)
	if errHost != nil {
		return errHost
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	primaryService := serviceType
	for _, sub := range spec.Subtypes {
		if clean := sanitizeSubtype(sub); clean != "" {
			primaryService += "," + clean
		}
	}

	registerProxy := a.registerProxy
	if registerProxy == nil {
		registerProxy = defaultRegisterProxy
	}

	var errRegister error
	server, errRegister = registerProxy(
		spec.InstanceName,
		primaryService,
		domain,
		spec.Port,
		advertisedHost,
		proxyIPs,
		spec.TextRecords,
		spec.Interfaces,
	)
	if errRegister != nil {
		return fmt.Errorf("discovery: failed to register primary service %s: %w", primaryService, errRegister)
	}
	if hr, ok := server.(*hostRecordResponder); ok && hr != nil {
		hr.mu.Lock()
		hr.baseHost = baseHost
		hr.osHost = cleanHost
		hr.attempt = initialAttempt
		if probeConflict != nil {
			hr.probeConflict = probeConflict
		}
		hr.mu.Unlock()
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("discovery: advertiser stopped before start completed")
	}
	a.server = server
	server = nil
	return nil
}

// Stop shuts down the mDNS advertisement server and sends goodbye packets.
func (a *ZeroconfAdvertiser) Stop() error {
	a.mu.Lock()
	a.closed = true
	server := a.server
	a.server = nil
	a.mu.Unlock()
	if server == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Warnf("discovery: panic during advertiser shutdown: %v", r)
		}
	}()
	server.Shutdown()
	return nil
}

// ZeroconfBrowser provides DNS-SD browsing with fallback support.
type ZeroconfBrowser struct {
	options []zeroconf.ClientOption
}

// NewZeroconfBrowser creates a new ZeroconfBrowser.
func NewZeroconfBrowser(ifaces ...net.Interface) *ZeroconfBrowser {
	var opts []zeroconf.ClientOption
	if len(ifaces) > 0 {
		opts = append(opts, zeroconf.SelectIfaces(ifaces))
	}
	return &ZeroconfBrowser{options: opts}
}

const (
	maxDiscoveredServices      = 256
	maxBrowseEntries           = 256
	maxBrowseTXTRecords        = 64
	maxBrowseTXTBytes          = 16 * 1024
	maxDiscoveredAddresses     = 32
	maxDiscoveredMetadataItems = 32
	maxMetadataItemBytes       = 64
)

func browseEntryWithinLimits(entry *zeroconf.ServiceEntry) bool {
	if entry == nil || len(entry.Text) > maxBrowseTXTRecords {
		return false
	}
	total := 0
	for _, record := range entry.Text {
		if len(record) > maxTXTRecordBytes {
			return false
		}
		total += len(record) + 1
		if total > maxBrowseTXTBytes {
			return false
		}
	}
	return true
}

// Browse performs a standard mDNS browse query for the given service type.
func (b *ZeroconfBrowser) Browse(ctx context.Context, serviceType, domain string) ([]DiscoveredService, error) {
	if ctx == nil {
		ctx = context.TODO()
	}
	if domain == "" {
		domain = DefaultDomain
	}
	if serviceType == "" {
		serviceType = DefaultServiceType
	}

	entries := make(chan *zeroconf.ServiceEntry, 32)
	browseCtx, cancelBrowse := context.WithCancel(ctx)
	defer cancelBrowse()

	var discovered []DiscoveredService
	var mu sync.Mutex
	seen := make(map[string]int)

	// Stop the underlying zeroconf client after a bounded number of entries. This
	// is separate from the caller's context so a LAN flood cannot grow the
	// dependency's internal sentEntries cache for the full browse lifetime.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		entriesSeen := 0
		for entry := range entries {
			if entriesSeen >= maxBrowseEntries {
				continue
			}
			entriesSeen++

			if browseEntryWithinLimits(entry) {
				svc := entryToDiscovered(entry)
				if svc.Port != 0 && (len(svc.IPv4) != 0 || len(svc.IPv6) != 0) {
					key := discoveredServiceKey(svc)
					mu.Lock()
					if index, ok := seen[key]; ok {
						mergeDiscoveredService(&discovered[index], svc)
					} else if len(discovered) < maxDiscoveredServices {
						seen[key] = len(discovered)
						discovered = append(discovered, svc)
					}
					mu.Unlock()
				}
			}

			if entriesSeen == maxBrowseEntries {
				cancelBrowse()
			}
		}
	}()

	errBrowse := zeroconf.Browse(browseCtx, serviceType, domain, entries, b.options...)
	if errBrowse != nil {
		// zeroconf may already have closed entries after a runtime error.
		closeBrowseEntries(entries)
		<-doneCh
		return nil, fmt.Errorf("discovery: browse query failed: %w", errBrowse)
	}

	<-doneCh

	return discovered, nil
}

func closeBrowseEntries(entries chan *zeroconf.ServiceEntry) {
	defer func() {
		_ = recover()
	}()
	close(entries)
}

func discoveredServiceKey(svc DiscoveredService) string {
	return strings.Join([]string{svc.InstanceName, svc.ServiceType, svc.Domain}, "\x00")
}

func mergeDiscoveredService(dst *DiscoveredService, src DiscoveredService) {
	if dst == nil {
		return
	}
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.Port != 0 {
		dst.Port = src.Port
	}
	dst.IPv4 = appendUniqueIPs(dst.IPv4, src.IPv4)
	dst.IPv6 = appendUniqueIPs(dst.IPv6, src.IPv6)
	if src.Product != "" {
		dst.Product = src.Product
	}
	if src.Version != "" {
		dst.Version = src.Version
	}
	if src.NodeRole != "" {
		dst.NodeRole = src.NodeRole
	}
	if dst.RawTXT == nil {
		dst.RawTXT = make(map[string]string)
	}
	mergeRawTXTRecords(dst.RawTXT, src.RawTXT)
	if value, ok := dst.RawTXT["auth_required"]; ok {
		dst.AuthRequired = strings.EqualFold(strings.TrimSpace(value), "true")
	}
	dst.AuthMethods = appendUniqueStrings(dst.AuthMethods, src.AuthMethods)
	dst.Protocols = appendUniqueStrings(dst.Protocols, src.Protocols)
	dst.Features = appendUniqueStrings(dst.Features, src.Features)
	if dst.Endpoints == nil {
		dst.Endpoints = make(map[string]string)
	}
	for key, value := range src.Endpoints {
		dst.Endpoints[key] = value
	}
}

func mergeRawTXTRecords(dst, src map[string]string) {
	totalBytes := rawTXTMapBytes(dst)
	for key, value := range src {
		if key == "" {
			continue
		}
		replacedBytes := 0
		_, exists := dst[key]
		if exists {
			replacedBytes = rawTXTRecordBytes(key, dst[key])
		} else if len(dst) >= maxBrowseTXTRecords {
			continue
		}
		nextBytes := totalBytes - replacedBytes + rawTXTRecordBytes(key, value)
		if nextBytes > maxBrowseTXTBytes {
			continue
		}
		dst[key] = value
		totalBytes = nextBytes
	}
}

func rawTXTMapBytes(records map[string]string) int {
	totalBytes := 0
	for key, value := range records {
		totalBytes += rawTXTRecordBytes(key, value)
	}
	return totalBytes
}

func rawTXTRecordBytes(key, value string) int {
	return len(key) + len(value) + 1
}

func appendUniqueIPs(dst, src []net.IP) []net.IP {
	for _, ip := range src {
		if ip == nil || len(dst) >= maxDiscoveredAddresses {
			continue
		}
		duplicate := false
		for _, existing := range dst {
			if existing.Equal(ip) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, append(net.IP(nil), ip...))
		}
	}
	return dst
}

func appendUniqueStrings(dst, src []string) []string {
	for _, value := range src {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxMetadataItemBytes || len(dst) >= maxDiscoveredMetadataItems {
			continue
		}
		duplicate := false
		for _, existing := range dst {
			if existing == value {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, value)
		}
	}
	return dst
}

func parseTXTList(value string) []string {
	var result []string
	for len(value) > 0 && len(result) < maxDiscoveredMetadataItems {
		item := value
		if before, after, ok := strings.Cut(value, ","); ok {
			item = before
			value = after
		} else {
			value = ""
		}
		item = strings.TrimSpace(item)
		if item == "" || len(item) > maxMetadataItemBytes {
			continue
		}
		result = appendUniqueStrings(result, []string{item})
	}
	return result
}

// BrowseWithFallback discovers all AI gateways on the LAN (_ai-gateway._tcp) and prioritizes CPA instances.
func (b *ZeroconfBrowser) BrowseWithFallback(ctx context.Context) ([]DiscoveredService, error) {
	return b.BrowseWithFallbackServiceType(ctx, DefaultServiceType)
}

// BrowseWithFallbackServiceType discovers services of the requested type and prioritizes CPA instances.
func (b *ZeroconfBrowser) BrowseWithFallbackServiceType(ctx context.Context, serviceType string) ([]DiscoveredService, error) {
	if strings.TrimSpace(serviceType) == "" {
		serviceType = DefaultServiceType
	}
	allGateways, errMain := b.Browse(ctx, serviceType, DefaultDomain)
	if errMain != nil && len(allGateways) == 0 {
		return nil, errMain
	}

	// Partition: prioritize CPA instances first, then other standard AI gateways
	var cpaGateways []DiscoveredService
	var otherGateways []DiscoveredService
	for _, gw := range allGateways {
		if gw.Product == ProductCPA {
			cpaGateways = append(cpaGateways, gw)
		} else {
			otherGateways = append(otherGateways, gw)
		}
	}

	return append(cpaGateways, otherGateways...), nil
}

// sanitizeEndpointPath validates that an endpoint path is a safe relative API path
// starting with '/' and containing no scheme, domain, protocol-relative prefixes, or traversal elements.
func sanitizeEndpointPath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) || strings.Contains(p, "://") || strings.Contains(p, "..") {
		return ""
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return ""
		}
	}
	return p
}

func entryToDiscovered(e *zeroconf.ServiceEntry) DiscoveredService {
	parsed := ParseTXTRecords(e.Text)

	port := e.Port
	if port < 1 || port > 65535 {
		port = 0
	}

	svc := DiscoveredService{
		InstanceName: e.Instance,
		ServiceType:  e.Service,
		Domain:       e.Domain,
		Host:         e.HostName,
		Port:         port,
		IPv4:         filterUsableIPs(e.AddrIPv4),
		IPv6:         filterUsableIPs(e.AddrIPv6),
		Product:      parsed["product"],
		Version:      parsed["version"],
		NodeRole:     parsed["node_role"],
		RawTXT:       parsed,
		Endpoints:    make(map[string]string),
	}

	if parsed["auth_required"] == "true" {
		svc.AuthRequired = true
	}
	if methods := parsed["auth_methods"]; methods != "" {
		svc.AuthMethods = parseTXTList(methods)
	}
	if protos := parsed["protocols"]; protos != "" {
		svc.Protocols = parseTXTList(protos)
	}
	if feats := parsed["features"]; feats != "" {
		svc.Features = parseTXTList(feats)
	}

	if v, ok := parsed["api_openai"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["openai"] = clean
		}
	}
	if v, ok := parsed["api_anthropic"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["anthropic"] = clean
		}
	}
	if v, ok := parsed["api_gemini"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["gemini"] = clean
		}
	}

	return svc
}

func filterUsableIPs(ips []net.IP) []net.IP {
	usable := make([]net.IP, 0, min(len(ips), maxDiscoveredAddresses))
	for _, ip := range ips {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		usable = append(usable, append(net.IP(nil), ip...))
		if len(usable) >= maxDiscoveredAddresses {
			break
		}
	}
	return usable
}
