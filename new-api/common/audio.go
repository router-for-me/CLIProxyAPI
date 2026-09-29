package common

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/abema/go-mp4"
	"github.com/go-audio/aiff"
	"github.com/go-audio/wav"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
	"github.com/pkg/errors"
	"github.com/tcolgate/mp3"
	"github.com/yapingcat/gomedia/go-codec"
)

// GetAudioDuration 使用纯 Go 库获取音频文件的时长（秒）。
// 它不再依赖外部的 ffmpeg 或 ffprobe 程序。
func GetAudioDuration(ctx context.Context, f io.ReadSeeker, ext string) (duration float64, err error) {
	SysLog(fmt.Sprintf("GetAudioDuration: ext=%s", ext))
	ext = strings.ToLower(ext)
	// 根据文件扩展名选择解析器
	switch ext {
	case ".mp3":
		duration, err = getMP3Duration(f)
	case ".wav":
		duration, err = getWAVDuration(f)
	case ".flac":
		duration, err = getFLACDuration(f)
	case ".m4a", ".mp4":
		duration, err = getM4ADuration(f)
	case ".ogg", ".oga", ".opus":
		duration, err = getOGGDuration(f)
		if err != nil {
			duration, err = getOpusDuration(f)
		}
	case ".aiff", ".aif", ".aifc":
		duration, err = getAIFFDuration(f)
	case ".webm":
		duration, err = getWebMDuration(f)
	case ".aac":
		duration, err = getAACDuration(f)
	default:
		return 0, fmt.Errorf("unsupported audio format: %s", ext)
	}
	SysLog(fmt.Sprintf("GetAudioDuration: duration=%f", duration))
	return duration, err
}

// getMP3Duration 解析 MP3 文件以获取时长。
// 注意：对于 VBR (Variable Bitrate) MP3，这个估算可能不完全精确，但通常足够好。
// FFmpeg 在这种情况下会扫描整个文件来获得精确值，但这里的库提供了快速估算。
func getMP3Duration(r io.Reader) (float64, error) {
	d := mp3.NewDecoder(r)
	var f mp3.Frame
	skipped := 0
	duration := 0.0

	for {
		if err := d.Decode(&f, &skipped); err != nil {
			if err == io.EOF {
				break
			}
			return 0, errors.Wrap(err, "failed to decode mp3 frame")
		}
		duration += f.Duration().Seconds()
	}
	return duration, nil
}

// getWAVDuration 解析 WAV 文件头以获取时长。
func getWAVDuration(r io.ReadSeeker) (float64, error) {
	// 1. 强制复位指针
	r.Seek(0, io.SeekStart)

	dec := wav.NewDecoder(r)

	// IsValidFile 会读取 fmt 块
	if !dec.IsValidFile() {
		return 0, errors.New("invalid wav file")
	}

	// 尝试寻找 data 块
	if err := dec.FwdToPCM(); err != nil {
		return 0, errors.Wrap(err, "failed to find PCM data chunk")
	}

	pcmSize := int64(dec.PCMSize)

	// 如果读出来的 Size 是 0，尝试用文件大小反推
	if pcmSize == 0 {
		// 获取文件总大小
		currentPos, _ := r.Seek(0, io.SeekCurrent) // 当前通常在 data chunk header 之后
		endPos, _ := r.Seek(0, io.SeekEnd)
		fileSize := endPos

		// 恢复位置（虽然如果不继续读也没关系）
		r.Seek(currentPos, io.SeekStart)

		// 数据区大小 ≈ 文件总大小 - 当前指针位置(即Header大小)
		// 注意：FwdToPCM 成功后，CurrentPos 应该刚好指向 Data 区数据的开始
		// 或者是 Data Chunk ID + Size 之后。
		// WAV Header 一般 44 字节。
		if fileSize > 44 {
			// 如果 FwdToPCM 成功，Reader 应该位于 data 块的数据起始处
			// 所以剩余的所有字节理论上都是音频数据
			pcmSize = fileSize - currentPos

			// 简单的兜底：如果算出来还是负数或0，强制按文件大小-44计算
			if pcmSize <= 0 {
				pcmSize = fileSize - 44
			}
		}
	}

	numChans := int64(dec.NumChans)
	bitDepth := int64(dec.BitDepth)
	sampleRate := float64(dec.SampleRate)

	if sampleRate == 0 || numChans == 0 || bitDepth == 0 {
		return 0, errors.New("invalid wav header metadata")
	}

	bytesPerFrame := numChans * (bitDepth / 8)
	if bytesPerFrame == 0 {
		return 0, errors.New("invalid byte depth calculation")
	}

	totalFrames := pcmSize / bytesPerFrame

	durationSeconds := float64(totalFrames) / sampleRate
	return durationSeconds, nil
}

// getFLACDuration 解析 FLAC 文件的 STREAMINFO 块。
func getFLACDuration(r io.Reader) (float64, error) {
	stream, err := flac.Parse(r)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse flac stream")
	}
	defer stream.Close()

	// 时长 = 总采样数 / 采样率
	duration := float64(stream.Info.NSamples) / float64(stream.Info.SampleRate)
	return duration, nil
}

// getM4ADuration 解析 M4A/MP4 文件的 'mvhd' box。
func getM4ADuration(r io.ReadSeeker) (float64, error) {
	// go-mp4 库需要 ReadSeeker 接口
	info, err := mp4.Probe(r)
	if err != nil {
		return 0, errors.Wrap(err, "failed to probe m4a/mp4 file")
	}
	// 时长 = Duration / Timescale
	return float64(info.Duration) / float64(info.Timescale), nil
}

// getOGGDuration 解析 OGG/Vorbis 文件以获取时长。
func getOGGDuration(r io.ReadSeeker) (float64, error) {
	// 重置 reader 到开头
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errors.Wrap(err, "failed to seek ogg file")
	}

	reader, err := oggvorbis.NewReader(r)
	if err != nil {
		return 0, errors.Wrap(err, "failed to create ogg vorbis reader")
	}

	// 计算时长 = 总采样数 / 采样率
	// 需要读取整个文件来获取总采样数
	channels := reader.Channels()
	sampleRate := reader.SampleRate()

	// 估算方法：读取到文件结尾
	var totalSamples int64
	buf := make([]float32, 4096*channels)
	for {
		n, err := reader.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, errors.Wrap(err, "failed to read ogg samples")
		}
		totalSamples += int64(n / channels)
	}

	duration := float64(totalSamples) / float64(sampleRate)
	return duration, nil
}

// getOpusDuration 解析 Opus 文件（在 OGG 容器中）以获取时长。
func getOpusDuration(r io.ReadSeeker) (float64, error) {
	// Opus 通常封装在 OGG 容器中
	// 我们需要解析 OGG 页面来获取时长信息
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errors.Wrap(err, "failed to seek opus file")
	}

	// 读取 OGG 页面头部
	var totalGranulePos int64
	buf := make([]byte, 27) // OGG 页面头部最小大小

	for {
		n, err := r.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, errors.Wrap(err, "failed to read opus/ogg page")
		}
		if n < 27 {
			break
		}

		// 检查 OGG 页面标识 "OggS"
		if string(buf[0:4]) != "OggS" {
			// 跳过一些字节继续寻找
			if _, err := r.Seek(-26, io.SeekCurrent); err != nil {
				break
			}
			continue
		}

		// 读取 granule position (字节 6-13, 小端序)
		granulePos := int64(binary.LittleEndian.Uint64(buf[6:14]))
		if granulePos > totalGranulePos {
			totalGranulePos = granulePos
		}

		// 读取段表大小
		numSegments := int(buf[26])
		segmentTable := make([]byte, numSegments)
		if _, err := io.ReadFull(r, segmentTable); err != nil {
			break
		}

		// 计算页面数据大小并跳过
		var pageSize int
		for _, segSize := range segmentTable {
			pageSize += int(segSize)
		}
		if _, err := r.Seek(int64(pageSize), io.SeekCurrent); err != nil {
			break
		}
	}

	// Opus 的采样率固定为 48000 Hz
	duration := float64(totalGranulePos) / 48000.0
	return duration, nil
}

// getAIFFDuration 解析 AIFF 文件头以获取时长。
func getAIFFDuration(r io.ReadSeeker) (float64, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errors.Wrap(err, "failed to seek aiff file")
	}

	dec := aiff.NewDecoder(r)
	if !dec.IsValidFile() {
		return 0, errors.New("invalid aiff file")
	}

	d, err := dec.Duration()
	if err != nil {
		return 0, errors.Wrap(err, "failed to get aiff duration")
	}

	return d.Seconds(), nil
}

// getWebMDuration 解析 WebM 文件以获取时长。
// WebM 使用 Matroska 容器格式
func getWebMDuration(r io.ReadSeeker) (float64, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errors.Wrap(err, "failed to seek webm file")
	}

	// WebM/Matroska 文件的解析比较复杂
	// 这里提供一个简化的实现，读取 EBML 头部
	// 对于完整的 WebM 解析，可能需要使用专门的库

	// 简单实现：查找 Duration 元素
	// WebM Duration 的 Element ID 是 0x4489
	// 这是一个简化版本，可能不适用于所有 WebM 文件
	buf := make([]byte, 8192)
	n, err := r.Read(buf)
	if err != nil && err != io.EOF {
		return 0, errors.Wrap(err, "failed to read webm file")
	}

	// 尝试查找 Duration 元素（这是一个简化的方法）
	// 实际的 WebM 解析需要完整的 EBML 解析器
	// 这里返回错误，建议使用专门的库
	if n > 0 {
		// 检查 EBML 标识
		if len(buf) >= 4 && binary.BigEndian.Uint32(buf[0:4]) == 0x1A45DFA3 {
			// 这是一个有效的 EBML 文件
			// 但完整解析需要更复杂的逻辑
			return 0, errors.New("webm duration parsing requires full EBML parser (consider using ffprobe for webm files)")
		}
	}

	return 0, errors.New("failed to parse webm file")
}

// getAACDuration 解析 AAC (ADTS格式) 文件以获取时长。
// 使用 gomedia 库来解析 AAC ADTS 帧
func getAACDuration(r io.ReadSeeker) (float64, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, errors.Wrap(err, "failed to seek aac file")
	}

	// 读取整个文件内容
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, errors.Wrap(err, "failed to read aac file")
	}

	var totalFrames int64
	var sampleRate int

	// 使用 gomedia 的 SplitAACFrame 函数来分割 AAC 帧
	codec.SplitAACFrame(data, func(aac []byte) {
		// 解析 ADTS 头部以获取采样率信息
		if len(aac) >= 7 {
			// 使用 ConvertADTSToASC 来获取音频配置信息
			asc, err := codec.ConvertADTSToASC(aac)
			if err == nil && sampleRate == 0 {
				sampleRate = codec.AACSampleIdxToSample(int(asc.Sample_freq_index))
			}
			totalFrames++
		}
	})

	if sampleRate == 0 || totalFrames == 0 {
		return 0, errors.New("no valid aac frames found")
	}

	// 每个 AAC ADTS 帧包含 1024 个采样
	totalSamples := totalFrames * 1024
	duration := float64(totalSamples) / float64(sampleRate)
	return duration, nil
}
