package download

import (
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io"
	"os"

	"github.com/apex/log"
	"github.com/develar/app-builder/pkg/util"
	"github.com/develar/errors"
	"github.com/develar/go-fs-util"
)

// ActualLocation represents server's status 200 or 206 response meta data. It never holds redirect responses
type ActualLocation struct {
	Url            string
	OutFileName    string
	isAcceptRanges bool
	StatusCode     int
	ContentLength  int64
	Parts          []*Part
}

func NewResolvedLocation(url string, contentLength int64, outFileName string, isAcceptRanges bool) ActualLocation {
	return ActualLocation{
		Url:            url,
		OutFileName:    outFileName,
		isAcceptRanges: isAcceptRanges,
		ContentLength:  contentLength,
	}
}

func (actualLocation *ActualLocation) computeParts(minPartSize int64) {
	if actualLocation.ContentLength < 0 {
		log.WithField("length", actualLocation.ContentLength).Warn("invalid content length, will be downloaded as one part")
		actualLocation.Parts = make([]*Part, 1)
		actualLocation.Parts[0] = &Part{
			Name:  actualLocation.OutFileName,
			Start: 0,
			End:   -1,
		}
		return
	}

	var partCount int
	contentLength := actualLocation.ContentLength
	if contentLength <= minPartSize {
		partCount = 1
	} else {
		partCount = int(contentLength / minPartSize)
		maxPartCount := getMaxPartCount()
		if partCount > maxPartCount {
			partCount = maxPartCount
		}
	}

	partSize := contentLength / int64(partCount)

	actualLocation.Parts = make([]*Part, partCount)

	start := int64(0)
	for i := 0; i < partCount; i++ {
		end := start + partSize
		if end > contentLength || i == (partCount - 1) {
			end = contentLength
		}

		var name string
		if i == 0 {
			name = actualLocation.OutFileName
		} else {
			name = fmt.Sprintf("%s.part%d", actualLocation.OutFileName, i)
		}

		actualLocation.Parts[i] = &Part{
			Name:  name,
			Start: start,
			End:   end,
		}

		start = end
	}
}

func (actualLocation *ActualLocation) deleteUnnecessaryParts() {
	for i := len(actualLocation.Parts) - 1; i >= 0; i-- {
		if actualLocation.Parts[i].Skip {
			actualLocation.Parts = append(actualLocation.Parts[:i], actualLocation.Parts[i+1:]...)
		}
	}
}

func (actualLocation *ActualLocation) concatenateParts(expectedSha512 string) error {
	hasCheckSum := len(expectedSha512) != 0

	fileMode := os.O_APPEND
	if hasCheckSum {
		if len(actualLocation.Parts) == 1 {
			fileMode = os.O_RDONLY
		} else {
			fileMode |= os.O_RDWR
		}
	} else {
		if len(actualLocation.Parts) == 1 {
			return nil
		}

		fileMode |= os.O_WRONLY
	}

	totalFile, err := os.OpenFile(actualLocation.Parts[0].Name, fileMode, 0644)
	if err != nil {
		return errors.WithStack(err)
	}

	defer util.Close(totalFile)

	buf := make([]byte, 32*1024)
	inputHash := sha512.New()
	if hasCheckSum {
		_, err = io.CopyBuffer(inputHash, totalFile, buf)
		if err != nil {
			return errors.WithStack(err)
		}
	}

	for i := 1; i < len(actualLocation.Parts); i++ {
		partFileName := actualLocation.Parts[i].Name
		partFile, err := os.Open(partFileName)
		if err != nil {
			return errors.WithStack(err)
		}

		var reader io.Reader
		if hasCheckSum {
			reader = io.TeeReader(partFile, inputHash)
		} else {
			reader = partFile
		}

		_, err = io.CopyBuffer(totalFile, reader, buf)
		err = fsutil.CloseAndCheckError(err, partFile)
		if err != nil {
			return errors.WithStack(err)
		}

		removeError := os.Remove(partFileName)
		if removeError != nil {
			log.WithFields(log.Fields{
				"partFile": partFileName,
				"error":    removeError,
			}).Error("cannot delete part file")
		}
	}

	if hasCheckSum {
		actualCheckSum := base64.StdEncoding.EncodeToString((inputHash).Sum(nil))
		if actualCheckSum != expectedSha512 {
			return errors.Errorf("sha512 checksum mismatch, expected %s, got %s", expectedSha512, actualCheckSum)
		}
	}

	return nil
}
