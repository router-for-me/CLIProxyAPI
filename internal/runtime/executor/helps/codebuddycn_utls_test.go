package helps

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

func TestCodeBuddyCNTLSClientHelloSpecMatchesCapturedProfile(t *testing.T) {
	spec := codeBuddyCNTLSClientHelloSpec()

	wantCiphers := []uint16{
		4866, 4867, 4865, 49199, 49195, 49200, 49196, 158,
		49191, 103, 49192, 107, 163, 159, 52393, 52392, 52394,
		49325, 49311, 49245, 49249, 49239, 49235, 162, 49324,
		49310, 49244, 49248, 49238, 49234, 49188, 106, 49187,
		64, 49162, 49172, 57, 56, 49161, 49171, 51, 50, 157,
		49309, 49233, 156, 49308, 49232, 61, 60, 53, 47,
	}
	if !reflect.DeepEqual(spec.CipherSuites, wantCiphers) {
		t.Fatalf("cipher suites = %v, want %v", spec.CipherSuites, wantCiphers)
	}

	wantExtensions := []uint16{65281, 0, 11, 10, 35, 22, 23, 13, 43, 45, 51}
	gotExtensions := make([]uint16, 0, len(spec.Extensions))
	for _, extension := range spec.Extensions {
		switch ext := extension.(type) {
		case *tls.RenegotiationInfoExtension:
			gotExtensions = append(gotExtensions, 65281)
		case *tls.SNIExtension:
			gotExtensions = append(gotExtensions, 0)
		case *tls.SupportedPointsExtension:
			gotExtensions = append(gotExtensions, 11)
		case *tls.SupportedCurvesExtension:
			gotExtensions = append(gotExtensions, 10)
		case *tls.SessionTicketExtension:
			gotExtensions = append(gotExtensions, 35)
		case *tls.GenericExtension:
			gotExtensions = append(gotExtensions, ext.Id)
		case *tls.ExtendedMasterSecretExtension:
			gotExtensions = append(gotExtensions, 23)
		case *tls.SignatureAlgorithmsExtension:
			gotExtensions = append(gotExtensions, 13)
		case *tls.SupportedVersionsExtension:
			gotExtensions = append(gotExtensions, 43)
		case *tls.PSKKeyExchangeModesExtension:
			gotExtensions = append(gotExtensions, 45)
		case *tls.KeyShareExtension:
			gotExtensions = append(gotExtensions, 51)
		case *tls.ALPNExtension:
			t.Fatal("CodeBuddy CN profile must not advertise ALPN")
		default:
			t.Fatalf("unrecognized extension type %T", extension)
		}
	}
	if !reflect.DeepEqual(gotExtensions, wantExtensions) {
		t.Fatalf("extensions = %v, want %v", gotExtensions, wantExtensions)
	}
}

func TestCodeBuddyCNTLSClientHelloWireJA3(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	defer func() { _ = listener.Close() }()

	ja3Ch := make(chan string, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		recordHeader := make([]byte, 5)
		if _, errRead := io.ReadFull(conn, recordHeader); errRead != nil {
			return
		}
		record := make([]byte, int(recordHeader[3])<<8|int(recordHeader[4]))
		if _, errRead := io.ReadFull(conn, record); errRead != nil {
			return
		}
		ja3, errJA3 := codeBuddyClientHelloJA3(record)
		if errJA3 == nil {
			ja3Ch <- ja3
		}
	}()

	client := &http.Client{Transport: newCodeBuddyCNRoundTripper("")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, port, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://localhost:"+port+"/", nil)
	_, _ = client.Do(req)

	select {
	case ja3 := <-ja3Ch:
		sum := md5.Sum([]byte(ja3)) //nolint:gosec -- JA3 uses MD5 by definition.
		if got := hex.EncodeToString(sum[:]); got != "944d1e1858cd278718f8a46b65d3212f" {
			t.Fatalf("JA3 MD5 = %s\nJA3 = %s", got, ja3)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ClientHello")
	}
}

func codeBuddyClientHelloJA3(handshake []byte) (string, error) {
	if len(handshake) < 39 || handshake[0] != 1 {
		return "", fmt.Errorf("invalid ClientHello")
	}
	offset := 4
	version := int(handshake[offset])<<8 | int(handshake[offset+1])
	offset += 2 + 32
	sessionIDLen := int(handshake[offset])
	offset += 1 + sessionIDLen
	cipherLen := int(handshake[offset])<<8 | int(handshake[offset+1])
	offset += 2
	ciphers := make([]string, 0, cipherLen/2)
	for end := offset + cipherLen; offset < end; offset += 2 {
		ciphers = append(ciphers, fmt.Sprint(int(handshake[offset])<<8|int(handshake[offset+1])))
	}
	compressionLen := int(handshake[offset])
	offset += 1 + compressionLen
	extLen := int(handshake[offset])<<8 | int(handshake[offset+1])
	offset += 2
	extEnd := offset + extLen
	var extensions, curves, points []string
	for offset < extEnd {
		extID := int(handshake[offset])<<8 | int(handshake[offset+1])
		dataLen := int(handshake[offset+2])<<8 | int(handshake[offset+3])
		offset += 4
		data := handshake[offset : offset+dataLen]
		extensions = append(extensions, fmt.Sprint(extID))
		if extID == 10 {
			for index := 2; index+1 < len(data); index += 2 {
				curves = append(curves, fmt.Sprint(int(data[index])<<8|int(data[index+1])))
			}
		}
		if extID == 11 {
			for index := 1; index < len(data); index++ {
				points = append(points, fmt.Sprint(data[index]))
			}
		}
		offset += dataLen
	}
	return fmt.Sprintf("%d,%s,%s,%s,%s", version, strings.Join(ciphers, "-"), strings.Join(extensions, "-"), strings.Join(curves, "-"), strings.Join(points, "-")), nil
}

func TestNewCodeBuddyCNHTTPClientUsesContextRoundTripperWithoutProxy(t *testing.T) {
	marker := codeBuddyRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(marker))
	client := NewCodeBuddyCNHTTPClient(ctx, nil, nil, 0)
	if reflect.ValueOf(client.Transport).Pointer() != reflect.ValueOf(marker).Pointer() {
		t.Fatalf("transport = %T, want context transport", client.Transport)
	}
}

type codeBuddyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f codeBuddyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
