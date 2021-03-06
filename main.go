package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/alecthomas/kingpin"
	"github.com/apex/log"
	"github.com/develar/app-builder/pkg/appimage"
	"github.com/develar/app-builder/pkg/blockmap"
	"github.com/develar/app-builder/pkg/dmg"
	"github.com/develar/app-builder/pkg/download"
	"github.com/develar/app-builder/pkg/elfExecStack"
	"github.com/develar/app-builder/pkg/fs"
	"github.com/develar/app-builder/pkg/icons"
	"github.com/develar/app-builder/pkg/linuxTools"
	"github.com/develar/app-builder/pkg/log-cli"
	"github.com/develar/app-builder/pkg/nodeModules"
	"github.com/develar/app-builder/pkg/snap"
	"github.com/develar/app-builder/pkg/util"
	"github.com/develar/errors"
)

var (
	app = kingpin.New("app-builder", "app-builder").Version("1.9.12")

	buildBlockMap            = app.Command("blockmap", "Generates file block map for differential update using content defined chunking (that is robust to insertions, deletions, and changes to input file)")
	buildBlockMapInFile      = buildBlockMap.Flag("input", "input file").Short('i').Required().String()
	buildBlockMapOutFile     = buildBlockMap.Flag("output", "output file").Short('o').String()
	buildBlockMapCompression = buildBlockMap.Flag("compression", "compression, one of: gzip, deflate").Short('c').Default("gzip").Enum("gzip", "deflate")
)

func main() {
	log_cli.InitLogger()

	if os.Getenv("SZA_ARCHIVE_TYPE") != "" {
		err := compress()
		if err != nil {
			util.LogErrorAndExit(err)
		}
		return
	}

	nodeModules.ConfigureCommand(app)
	download.ConfigureCommand(app)
	download.ConfigureDownloadResolvedFilesCommand(app)
	configurePrefetchToolsCommand(app)
	download.ConfigureArtifactCommand(app)
	ConfigureCopyCommand(app)
	appimage.ConfigureCommand(app)
	snap.ConfigureCommand(app)
	icons.ConfigureCommand(app)
	dmg.ConfigureCommand(app)
	elfExecStack.ConfigureCommand(app)

	command, err := app.Parse(os.Args[1:])
	if err != nil {
		util.LogErrorAndExit(err)
	}

	switch command {
	case buildBlockMap.FullCommand():
		err := doBuildBlockMap()
		if err != nil {
			log.Fatalf("%+v\n", err)
		}

	case buildBlockMap.FullCommand():
		err := doBuildBlockMap()
		if err != nil {
			log.Fatalf("%+v\n", err)
		}
	}
}

func ConfigureCopyCommand(app *kingpin.Application) {
	command := app.Command("copy", "Copy file or dir.")
	from := command.Flag("from", "").Required().Short('f').String()
	to := command.Flag("to", "").Required().Short('t').String()
	isUseHardLinks := command.Flag("hard-link", "Whether to use hard-links if possible").Bool()

	command.Action(func(context *kingpin.ParseContext) error {
		var fileCopier fs.FileCopier
		fileCopier.IsUseHardLinks = *isUseHardLinks
		return errors.WithStack(fileCopier.CopyDirOrFile(*from, *to))
	})
}

func compress() error {
	args := []string{"a", "-si", "-so", "-t" + util.GetEnvOrDefault("SZA_ARCHIVE_TYPE", "xz"), "-mx" + util.GetEnvOrDefault("SZA_COMPRESSION_LEVEL", "9"), "dummy"}
	args = append(args, os.Args[1:]...)

	command := exec.Command(util.GetEnvOrDefault("SZA_PATH", "7za"), args...)
	command.Stderr = os.Stderr

	stdin, err := command.StdinPipe()
	if nil != err {
		return errors.WithStack(err)
	}

	stdout, err := command.StdoutPipe()
	if nil != err {
		return errors.WithStack(err)
	}

	err = command.Start()
	if err != nil {
		return errors.WithStack(err)
	}

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		defer util.Close(stdin)
		io.Copy(stdin, os.Stdin)
	}()

	go func() {
		defer waitGroup.Done()
		io.Copy(os.Stdout, stdout)
	}()

	waitGroup.Wait()
	err = command.Wait()
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func doBuildBlockMap() error {
	var compressionFormat blockmap.CompressionFormat
	switch *buildBlockMapCompression {
	case "gzip":
		compressionFormat = blockmap.GZIP
	case "deflate":
		compressionFormat = blockmap.DEFLATE
	default:
		return fmt.Errorf("unknown compression format %s", *buildBlockMapCompression)
	}

	inputInfo, err := blockmap.BuildBlockMap(*buildBlockMapInFile, blockmap.DefaultChunkerConfiguration, compressionFormat, *buildBlockMapOutFile)
	if err != nil {
		return err
	}

	return util.WriteJsonToStdOut(inputInfo)
}

func configurePrefetchToolsCommand(app *kingpin.Application) {
	command := app.Command("prefetch-tools", "Prefetch all required tools")
	osName := command.Flag("osName", "").Default(runtime.GOOS).Enum("darwin", "linux", "win32")
	command.Action(func(context *kingpin.ParseContext) error {
		_, err := linuxTools.GetAppImageToolDir()
		if err != nil {
			return errors.WithStack(err)
		}

		_, err = snap.ResolveTemplateFile("", "electron2", "")
		if err != nil {
			return errors.WithStack(err)
		}

		_, err = download.DownloadFpm()
		if err != nil {
			return errors.WithStack(err)
		}
		_, err = download.DownloadZstd(*osName)
		if err != nil {
			return errors.WithStack(err)
		}
		return nil
	})
}