package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	_ "github.com/dsoprea/go-exif/v3"
)

const (
	dateTimeLayout    = "2006-01-02_15-04-05"
	duplicateSuffix   = "-"
	metadataExtension = "xmp"
)

var (
	parserFuncs = []func(string) (time.Time, error){
		func(createDate string) (time.Time, error) {
			return time.Parse("2006:01:02 15:04:05-07:00", createDate)
		},
		func(createDate string) (time.Time, error) {
			dt, err := time.ParseInLocation("2006:01:02 15:04:05", createDate, time.Local)
			if err == nil {
				log.Printf("Parsed date %s in local timezone as it doesn't have a timezone\n", createDate)
			}
			return dt, err
		},
	}
)

type CliArgs struct {
	DryRun bool
	Dir    string `arg:"" type:"existingdir"`
}

var (
	pictureExtensionsWhitelist = map[string]bool{
		".jpeg": true,
		".jpg":  true,
		".cr2":  true,
		".cr3":  true,
	}
)

type StringSet map[string]struct{}

func newStringSet() StringSet {
	return make(StringSet)
}

func (set *StringSet) Contains(s string) bool {
	_, found := (*set)[s]
	return found
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
	RenamedFilename  string
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

type PhotoExifData struct {
	CreateDate *string
}

func tryParseDateTime(createDate string) (time.Time, error) {
	for _, f := range parserFuncs {
		dt, err := f(createDate)
		if err == nil {
			return dt, nil
		}
	}
	return time.Time{}, errors.New(fmt.Sprintf("error parsing create date %s", createDate))
}

func getPhotoDateTime(filepath string) (time.Time, error) {
	cmd := exec.Command("exiftool", "-time:all", "-struct", "-j", filepath)
	out, err := cmd.Output()
	checkErr(err)

	var data []PhotoExifData
	err = json.Unmarshal(out, &data)
	checkErr(err)

	if len(data) == 0 || data[0].CreateDate == nil {
		return time.Time{}, errors.New("no CreateDate available")
	}

	createDate := *data[0].CreateDate
	log.Printf("Trying to parse create date %s for file %s\n", createDate, filepath)
	dt, err := tryParseDateTime(createDate)
	if err != nil {
		return time.Time{}, err
	}
	log.Printf("Parsed create date as %s for file %s\n", createDate, filepath)
	return dt, nil
}

func main() {
	args := &CliArgs{}
	kong.Parse(args)
	log.Printf("Args: %v\n", args)

	directory, err := filepath.Abs(args.Dir)
	checkErr(err)

	log.Printf("Looking for files in directory: %s...\n", directory)

	fileInfos, err := os.ReadDir(directory)
	checkErr(err)

	log.Println("Done.")

	// Gather all the original files and set up the renames
	finalFilenames := newStringSet()
	fileRenames := make([]*PhotoRename, 0)
	for _, f := range fileInfos {
		filename := f.Name()
		fp := filepath.Join(directory, filename)

		log.Printf("Processing %s...\n", fp)

		if isPictureFile(filename) {
			photoDateTime, err := getPhotoDateTime(fp)
			if err != nil {
				log.Printf("Failed to get datetime for %s, %v\n", fp, err)
				finalFilenames.Add(filename)
				continue
			}

			photoRename := newPhotoRename(filename, photoDateTime)

			if photoRename.IsAlreadyFormatted() {
				log.Printf("Photo %s already formatted.\n", filename)
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

	var metadataRenames = make([]*PhotoRename, 0)
	for _, rename := range fileRenames {
		metadataFilename := fmt.Sprintf("%s.%s", getNameWithoutExtension(rename.OriginalFilename), metadataExtension)
		metadataFilepath := filepath.Join(directory, metadataFilename)
		exists, err := fileExists(metadataFilepath)
		checkErr(err)
		if exists {
			newMetadataFilename := fmt.Sprintf("%s.%s", getNameWithoutExtension(rename.RenamedFilename), metadataExtension)
			newMetadataFilepath := filepath.Join(directory, newMetadataFilename)
			newMetadataExists, err := fileExists(newMetadataFilepath)
			checkErr(err)
			if newMetadataExists {
				log.Fatalf("Metadata %s already exists\n", newMetadataFilepath)
			}

			metadataRename := newPhotoRename(metadataFilename, rename.PhotoCaptureTime)
			metadataRename.RenamedFilename = newMetadataFilename
			metadataRenames = append(metadataRenames, metadataRename)
		}
	}
	fileRenames = append(fileRenames, metadataRenames...)

	for _, rename := range fileRenames {
		if len(rename.RenamedFilename) == 0 {
			panic(fmt.Sprintf("Invalid rename '%v'", rename))
		}
		processRename(rename, directory, args.DryRun)
	}
}

func fileExists(filepath string) (bool, error) {
	_, err := os.Stat(filepath)

	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, err
}

func processRename(rename *PhotoRename, directory string, dryRun bool) {

	oldPath := filepath.Join(directory, rename.OriginalFilename)
	newPath := filepath.Join(directory, rename.RenamedFilename)

	exists, err := fileExists(newPath)
	checkErr(err)

	if exists {
		panic(fmt.Sprintf("Renamed file '%s' already exists", newPath))
	}

	if dryRun {
		log.Printf("DRY RUN: Would be renaming %s to %s\n", oldPath, newPath)
	} else {
		log.Printf("Renaming %s to %s\n", oldPath, newPath)
		err = os.Rename(oldPath, newPath)
	}
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}
