package fs

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/develar/errors"
	"github.com/develar/go-fs-util"
)

func ReadFile(file string, size int) ([]byte, error) {
	reader, err := os.Open(file)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	result := make([]byte, size)
	_, err = reader.Read(result)
	return result, fsutil.CloseAndCheckError(err, reader)
}

func RemoveByGlob(fileGlob string) error {
	if !strings.HasSuffix(fileGlob, "*") {
		return errors.WithStack(os.RemoveAll(fileGlob))
	}

	dir := filepath.Dir(fileGlob)
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		} else {
			return errors.WithStack(err)
		}
	}

	for _, file := range files {
		matched, err := filepath.Match(fileGlob, file.Name())
		if err != nil {
			return err
		}
		if !matched {
			continue
		}

		absoluteChildFile := filepath.Join(dir, file.Name())
		if file.IsDir() {
			err = os.RemoveAll(absoluteChildFile)
		} else {
			err = syscall.Unlink(absoluteChildFile)
		}
		if err != nil {
			return err
		}
	}

	return nil
}
