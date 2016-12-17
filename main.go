package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"path"
	"strings"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"
	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/mknote"
)

const (
	version = "0.0.1"
	dateTimeLayout = "2006-01-02_15-04-05"
	duplicateSuffix = "-"
)

var (
	debugFlag = kingpin.Flag("verbose", "Enable verbose output.").Short('v').Bool()
	dryRunFlag = kingpin.Flag("dry-run", "Enable dry run mode.").Short('d').Bool()
	dirArg    = kingpin.Arg("directory", "Directory to use.").Required().ExistingDir()

	pictureExtensionsWhitelist = map[string]bool{
		".jpeg": true,
		".jpg":  true,
		".cr2":  true,
	}
)


type StringSet map[string]struct{}

func newStringSet() StringSet {
	return make(StringSet)
}

func (set *StringSet) Contains(s string) bool {
	_, found := (*set)[s];
	return found;
}

func (set *StringSet) Add(s string) {
	(*set)[s] = struct{}{}
}

func (set *StringSet) Remove(s string) {
	delete(*set, s)
}

func (set *StringSet) Iter() <-chan interface{} {
	ch := make(chan interface{})
	go func() {
		for element := range *set {
			ch <- element
		}
		close(ch)
	}()

	return ch
}

type PhotoRename struct {
	OriginalFilename string
	PhotoCaptureTime time.Time
	RenamedFilename string
}

func (photoRename *PhotoRename) GetFormattedDateTime() string {
	return photoRename.PhotoCaptureTime.Format(dateTimeLayout)
}

func (photoRename *PhotoRename) IsAlreadyFormatted() bool {
	exifFormattedDateTime := photoRename.GetFormattedDateTime()
	filenameWithoutExtension := getNameWithoutExtension(photoRename.OriginalFilename)

	// Ignore duplicate suffixes completely. This is a bit of a shortcut because
	// there could be a file that ends in "---" but is the only one (not a duplicate),
	// but that should be fine.
	trimmedFilename := strings.TrimRight(filenameWithoutExtension, duplicateSuffix)
	return exifFormattedDateTime == trimmedFilename
}

func (photoRename *PhotoRename) GetFormattedFilename(duplicateSuffixes int) string {
	formattedDateTime := photoRename.GetFormattedDateTime()
	suffix := strings.Repeat(duplicateSuffix, duplicateSuffixes)
	extension := getExtension(photoRename.OriginalFilename)
	return fmt.Sprintf("%s%s%s", formattedDateTime, suffix, extension)
}

func newPhotoRename(originalFilename string, photoCaptureTime time.Time) *PhotoRename {
	return &PhotoRename{OriginalFilename: originalFilename, PhotoCaptureTime: photoCaptureTime}
}

func getExtension(filename string) string {
	return filepath.Ext(filename)
}

func getNameWithoutExtension(filename string) string {
	extension := getExtension(filename)
	extensionIndex := len(filename) - len(extension)
	return filename[0:extensionIndex]
}

func isPictureFile(filename string) bool {
	extension := strings.ToLower(path.Ext(filename))
	_, ok := pictureExtensionsWhitelist[extension]
	return ok
}

func getPhotoDateTime(filepath string) (time.Time, error) {
	f, err := os.Open(filepath)
	checkErr(err)

	x, err := exif.Decode(f)
	if err != nil {
		return time.Time{}, err
	}

	t, err := x.DateTime()
	if err != nil {
		return time.Time{}, err
	}

	return t, nil
}

func main() {
	kingpin.Version(version)
	kingpin.Parse()

	directory, err := filepath.Abs(*dirArg)
	checkErr(err)

	fmt.Printf("Looking for files in directory: %s...\n", directory)

	fileInfos, err := ioutil.ReadDir(directory)
	checkErr(err)

	fmt.Println("Done.")

	exif.RegisterParsers(mknote.All...)

	// Gather all of the original files and set up the renames
	finalFilenames := newStringSet()
	fileRenames := make([]*PhotoRename, 0)
	for _, f := range fileInfos {
		filename := f.Name()
		filepath := filepath.Join(directory, filename)

		fmt.Printf("Processing '%s'.\n", filepath)

		if isPictureFile(filename) {
			photoDateTime, err := getPhotoDateTime(filepath)
			if err != nil {
				fmt.Printf("Failed to get datetime for '%s', '%v'\n", filepath, err)
				finalFilenames.Add(filename)
				continue
			}

			photoRename := newPhotoRename(filename, photoDateTime)

			if photoRename.IsAlreadyFormatted() {
				fmt.Printf("Photo '%s' already formatted.\n", filename)
				finalFilenames.Add(filename)
			} else {
				fileRenames = append(fileRenames, newPhotoRename(filename, photoDateTime))
			}
		}
	}

	for _, rename := range fileRenames {
		dupeCount := 0
		for {
			formattedFilename := rename.GetFormattedFilename(dupeCount)

			if !finalFilenames.Contains(formattedFilename) {
				finalFilenames.Add(formattedFilename)
				rename.RenamedFilename = formattedFilename
				break
			}

			dupeCount++
		}
	}

	for _, rename := range fileRenames {
		processRename(rename, directory, *dryRunFlag)
		if *dryRunFlag {
		} else {

		}
	}
}

func fileExists(filepath string) (bool, error) {
	_, err := os.Stat(filepath);

	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, err
}

func processRename(rename *PhotoRename, directory string, dryRun bool) {
	if len(rename.RenamedFilename) == 0 {
		panic(fmt.Sprintf("Renamed filename is empty for '%v'", rename.RenamedFilename))
	}

	oldPath := filepath.Join(directory, rename.OriginalFilename)
	newPath := filepath.Join(directory, rename.RenamedFilename)

	exists, err := fileExists(newPath)
	checkErr(err)

	if exists {
		panic(fmt.Sprintf("Renamed file '%s' already exists", newPath))
	}

	if dryRun {
		fmt.Printf("DRY RUN: Would be renaming '%s' to '%s'\n", oldPath, newPath)
	} else {
		fmt.Printf("Renaming '%s' to '%s'\n", oldPath, newPath)
		os.Rename(oldPath, newPath)
	}
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}
