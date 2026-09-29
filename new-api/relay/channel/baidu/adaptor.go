package baidu

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	//TODO implement me
	panic("implement me")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {

}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	// https://cloud.baidu.com/doc/WENXINWORKSHOP/s/clntwmv7t
	suffix := "chat/"
	if strings.HasPrefix(info.UpstreamModelName, "Embedding") {
		suffix = "embeddings/"
	}
	if strings.HasPrefix(info.UpstreamModelName, "bge-large") {
		suffix = "embeddings/"
	}
	if strings.HasPrefix(info.UpstreamModelName, "tao-8k") {
		suffix = "embeddings/"
	}
	switch info.UpstreamModelName {
	case "ERNIE-4.0":
		suffix += "completions_pro"
	case "ERNIE-Bot-4":
		suffix += "completions_pro"
	case "ERNIE-Bot":
		suffix += "completions"
	case "ERNIE-Bot-turbo":
		suffix += "eb-instant"
	case "ERNIE-Speed":
		suffix += "ernie_speed"
	case "ERNIE-4.0-8K":
		suffix += "completions_pro"
	case "ERNIE-3.5-8K":
		suffix += "completions"
	case "ERNIE-3.5-8K-0205":
		suffix += "ernie-3.5-8k-0205"
	case "ERNIE-3.5-8K-1222":
		suffix += "ernie-3.5-8k-1222"
	case "ERNIE-Bot-8K":
		suffix += "ernie_bot_8k"
	case "ERNIE-3.5-4K-0205":
		suffix += "ernie-3.5-4k-0205"
	case "ERNIE-Speed-8K":
		suffix += "ernie_speed"
	case "ERNIE-Speed-128K":
		suffix += "ernie-speed-128k"
	case "ERNIE-Lite-8K-0922":
		suffix += "eb-instant"
	case "ERNIE-Lite-8K-0308":
		suffix += "ernie-lite-8k"
	case "ERNIE-Tiny-8K":
		suffix += "ernie-tiny-8k"
	case "BLOOMZ-7B":
		suffix += "bloomz_7b1"
	case "Embedding-V1":
		suffix += "embedding-v1"
	case "bge-large-zh":
		suffix += "bge_large_zh"
	case "bge-large-en":
		suffix += "bge_large_en"
	case "tao-8k":
		suffix += "tao_8k"
	default:
		suffix += strings.ToLower(info.UpstreamModelName)
	}
	fullRequestURL := fmt.Sprintf("%s/rpc/2.0/ai_custom/v1/wenxinworkshop/%s", info.ChannelBaseUrl, suffix)
	var accessToken string
	var err error
	if accessToken, err = getBaiduAccessToken(info.ApiKey); err != nil {
		return "", err
	}
	fullRequestURL += "?access_token=" + accessToken
	return fullRequestURL, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch info.RelayMode {
	default:
		baiduRequest := requestOpenAI2Baidu(*request)
		return baiduRequest, nil
	}
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	baiduEmbeddingRequest := embeddingRequestOpenAI2Baidu(request)
	return baiduEmbeddingRequest, nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	// TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.IsStream {
		err, usage = baiduStreamHandler(c, info, resp)
	} else {
		switch info.RelayMode {
		case constant.RelayModeEmbeddings:
			err, usage = baiduEmbeddingHandler(c, info, resp)
		default:
			err, usage = baiduHandler(c, info, resp)
		}
	}
	return
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
