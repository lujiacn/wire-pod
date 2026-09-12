package wirepod_ttr

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

// CaptureEnvPhoto connects to the robot and captures a single image from its
// camera, completely silently - no mirror mode, no countdown, no shutter
// animation, no sound. The returned string is a base64-encoded JPEG, or "" on
// any failure (in which case the LLM request just proceeds without a photo).
func CaptureEnvPhoto(esn string) string {
	var guid string
	var target string
	matched := false
	for _, bot := range vars.BotInfo.Robots {
		if esn == bot.Esn {
			guid = bot.GUID
			target = bot.IPAddress + ":443"
			matched = true
			break
		}
	}
	if !matched {
		logger.Println("(photo) bot " + esn + " is not in the SDK info list, not capturing photo")
		return ""
	}
	robot, err := vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
	if err != nil {
		logger.Println("(photo) error connecting to robot for photo: " + err.Error())
		return ""
	}
	resp, err := robot.Conn.CaptureSingleImage(
		context.Background(),
		&vectorpb.CaptureSingleImageRequest{
			EnableHighResolution: false,
		},
	)
	if err != nil {
		logger.Println("(photo) error capturing image: " + err.Error())
		return ""
	}
	if len(resp.Data) == 0 {
		logger.Println("(photo) captured image is empty")
		return ""
	}
	logger.Println("(photo) captured environment photo, " + fmt.Sprint(len(resp.Data)) + " bytes")
	return base64.StdEncoding.EncodeToString(resp.Data)
}
