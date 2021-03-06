package download

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin"
	"github.com/apex/log"
	"github.com/develar/app-builder/pkg/util"
	"github.com/develar/errors"
	"github.com/mitchellh/go-homedir"
	"github.com/zieckey/goini"
)

//noinspection SpellCheckingInspection
const (
	maxRedirects = 10
	minPartSize  = 5 * 1024 * 1024
	userAgent    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_3) AppleWebKit/604.5.6 (KHTML, like Gecko) Version/11.0.3 Safari/604.5.6"
)

func getMaxPartCount() int {
	const maxPartCount = 8
	result := runtime.NumCPU() * 2
	if result > maxPartCount {
		return maxPartCount
	} else {
		return result
	}
}

func ConfigureCommand(app *kingpin.Application) {
	command := app.Command("download", "Download file.")
	fileUrl := command.Flag("url", "The URL.").Short('u').Required().String()
	output := command.Flag("output", "The output file.").Short('o').Required().String()
	sha512 := command.Flag("sha512", "The expected sha512 of file.").String()

	command.Action(func(context *kingpin.ParseContext) error {
		return errors.WithStack(NewDownloader().Download(*fileUrl, *output, *sha512))
	})
}

type Downloader struct {
	client    *http.Client
	transport *http.Transport
}

func NewDownloader() Downloader {
	transport := &http.Transport{
		Proxy:               proxyFromEnvironmentAndNpm,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     30 * time.Second,
	}
	return Downloader{
		transport: transport,
		client: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: transport,
		},
	}
}

func (t Downloader) Download(url string, output string, sha512 string) error {
	dir := filepath.Dir(output)
	err := os.MkdirAll(dir, 0777)
	if err != nil {
		return errors.WithStack(err)
	}

	actualLocation, err := t.follow(url, userAgent, output)
	if err != nil {
		return errors.WithStack(err)
	}

	return t.DownloadResolved(actualLocation, sha512)
}

func (t Downloader) DownloadResolved(location *ActualLocation, sha512 string) error {
	downloadContext, cancel := context.WithCancel(context.Background())
	go onCancelSignal(cancel)

	location.computeParts(minPartSize)
	log.WithFields(&log.Fields{
		"url": location.Url,
		"parts": len(location.Parts),
	}).Debug("download")
	err := util.MapAsyncConcurrency(len(location.Parts), getMaxPartCount(), func(index int) (func() error, error) {
		part := location.Parts[index]
		return func() error {
			err := part.download(downloadContext, location.Url, index, t.client)
			if err != nil {
				part.isFail = true
				log.WithFields(log.Fields{
					"id":    index,
					"error": err,
				}).Debug("part download error")
			}
			return err
		}, nil
	})

	if err != nil {
		return errors.WithStack(err)
	}

	for _, part := range location.Parts {
		if part.isFail {
			cancel()
			break
		}
	}

	location.deleteUnnecessaryParts()
	err = location.concatenateParts(sha512)
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func (t Downloader) follow(initialUrl, userAgent, outFileName string) (*ActualLocation, error) {
	currentUrl := initialUrl
	redirectsFollowed := 0
	for {
		if currentUrl != initialUrl {
			log.WithFields(log.Fields{
				"initialUrl": initialUrl,
				"currentUrl": currentUrl,
			}).Debug("computing effective URL")
		}

		// should use GET instead of HEAD because ContentLength maybe omitted for HEAD requests
		// https://stackoverflow.com/questions/3854842/content-length-header-with-head-requests
		req, err := http.NewRequest(http.MethodGet, currentUrl, nil)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		req.Header.Set("User-Agent", userAgent)
		actualLocation, err := func() (*ActualLocation, error) {
			response, err := t.client.Do(req)
			if response != nil {
				util.Close(response.Body)
			}

			if err != nil {
				return nil, errors.WithStack(err)
			}

			if isRedirect(response.StatusCode) {
				loc, err := response.Location()
				if err != nil {
					return nil, errors.WithStack(err)
				}

				currentUrl = loc.String()
				return nil, nil
			} else if response.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("resolve request failed with status code %d", response.StatusCode)
			}

			actualLocation := NewResolvedLocation(currentUrl, response.ContentLength, outFileName, response.Header.Get("Accept-Ranges") != "")
			var length string
			if response.ContentLength < 0 {
				length = "unknown"
			} else {
				length = fmt.Sprintf("%d", response.ContentLength)
			}

			log.WithFields(log.Fields{
				"length":       length,
				"content-type": response.Header.Get("Content-Type"),
				"url":          initialUrl,
			}).Debug("downloading")

			if !actualLocation.isAcceptRanges {
				log.Warn("server doesn't support ranges")
			}
			return &actualLocation, nil
		}()

		if err != nil {
			return nil, errors.WithStack(err)
		}

		if actualLocation != nil {
			return actualLocation, nil
		}

		redirectsFollowed++
		if redirectsFollowed > maxRedirects {
			return nil, errors.Errorf("maximum number of redirects (%d) followed", maxRedirects)
		}
	}
}

func onCancelSignal(cancel context.CancelFunc) {
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig := <-signals
	log.Infof("%v: canceling...\n", sig)
}

func isRedirect(status int) bool {
	return status > 299 && status < 400
}

func proxyFromEnvironmentAndNpm(req *http.Request) (*url.URL, error) {
	if os.Getenv("NO_PROXY") == "*" {
		return nil, nil
	}

	result, err := http.ProxyFromEnvironment(req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if result != nil {
		return result, nil
	}

	result, err = proxyFromNpm()
	if err != nil {
		log.WithError(err).Error("cannot detect npm proxy")
		return nil, nil
	}
	return result, nil
}

func proxyFromNpm() (*url.URL, error) {
	userHomeDir, err := homedir.Dir()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	ini := goini.New()
	err = ini.ParseFile(filepath.Join(userHomeDir, ".npmrc"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.WithStack(err)
	}

	v, ok := ini.Get("https-proxy")
	if !ok {
		v, _ = ini.Get("proxy")
	}

	if len(v) == 0 || v == "false" || v == "true" {
		return nil, nil
	}

	parsed, err := url.Parse(v)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return parsed, nil
}
