package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var repoURL = "https://koti.kapsi.fi/darkon/polloeskadroona/repo/updater.json"

type repository struct {
	DownloadRoot string
	Files        [][]string
}

type repositoryFile struct {
	Name string
	Hash string
}

func (f repositoryFile) HasValidPath() bool {
	currentPath, _ := os.Getwd()
	absolutePath, err := filepath.Abs(f.Name)
	if err != nil {
		fmt.Println("Unable to resolve path for", f.Name)
		return false
	}
	cleaned := path.Clean(absolutePath)
	return strings.Contains(cleaned, currentPath)
}

func (f repositoryFile) CheckHash(i *os.File) bool {
	return calculateHash(i) == f.Hash
}

func main() {
	var flagRepoURL = flag.String("repoUrl", "", "Set URL to custom repository json")
	var flagDirectoryName = flag.String("createRepo", "", "Directory to create a repository json from")
	var flagOutputName = flag.String("output", "updater.json", "Name of the json file for -createRepo")

	flag.Parse()

	if len(*flagRepoURL) > 0 {
		repoURL = *flagRepoURL
	}

	if len(*flagDirectoryName) == 0 {
		updateFiles()
	} else {
		createRepo(*flagDirectoryName, *flagOutputName)
	}
}

func createRepo(directoryName string, outputName string) {
	newRepo := repository{}
	newRepo.DownloadRoot = "https://koti.kapsi.fi/darkon/polloeskadroona/repo/"
	filepath.Walk(directoryName, func(currentPath string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}

		currentFile, openError := os.Open(currentPath)
		if openError != nil {
			return openError
		}
		defer currentFile.Close()

		hash := calculateHash(currentFile)
		currentPathSlash := filepath.ToSlash(currentPath)
		fmt.Println(currentPathSlash, ":", hash)
		newRepo.Files = append(newRepo.Files, []string{currentPathSlash, hash})
		return nil
	})

	repoBytes, marshalError := json.Marshal(newRepo)
	if marshalError != nil {
		fmt.Println(marshalError)
		return
	}
	ioutil.WriteFile(outputName, repoBytes, 0644)
	fmt.Println("\nWriting output to", outputName)
}

// go doesn't have "str in []string" check built in
func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

func updateFiles() {
	fmt.Println("Repository:", repoURL)

	downloadRoot, listOfRepositoryFiles := getRepositoryContent()
	if listOfRepositoryFiles == nil {
		return
	}

	var downloadFiles []repositoryFile
	downloadErrors := 0

	var directoriesToPrune []string

	fmt.Println("")

	// check existing files and their checksum
	for _, rf := range listOfRepositoryFiles {

		if !rf.HasValidPath() {
			// invalid path, ignore
			continue
		}

		fmt.Print(rf.Name + " : ")
		var rfStatus string

		// collect directory name to list of directories for pruning
		pathParts := strings.Split(rf.Name, "/")
		if !stringInSlice(pathParts[0], directoriesToPrune) {
			directoriesToPrune = append(directoriesToPrune, pathParts[0])
		}

		existingFile, openError := os.Open(rf.Name)

		if os.IsNotExist(openError) {
			downloadFiles = append(downloadFiles, rf)
			fmt.Println("Download")
			continue
		} else if openError != nil {
			fmt.Println("Skip:", openError)
			continue
		}

		if rf.CheckHash(existingFile) {
			rfStatus = "OK"
		} else {
			rfStatus = "Download (Changed)"
			downloadFiles = append(downloadFiles, rf)
		}
		existingFile.Close()
		fmt.Println(rfStatus)
	}

	// remove any file that is not part of the repository. directories will
	// not be removed
	fmt.Println("")
	fmt.Println("Pruning non-repository files")
	for _, pruneDir := range directoriesToPrune {
		if _, err := os.Stat(pruneDir); os.IsNotExist(err) {
			continue
		}
		filepath.Walk(pruneDir, func(currentPath string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}
			currentPathSlash := filepath.ToSlash(currentPath)
			belongsToRepo := false
			for _, rf := range listOfRepositoryFiles {
				if currentPathSlash == rf.Name {
					belongsToRepo = true
				}
			}
			if !belongsToRepo {
				fmt.Println("Removing", currentPathSlash)
				if removeError := os.RemoveAll(currentPathSlash); removeError != nil {
					return removeError
				}
			}
			return nil
		})
	}

	// download files that are missing or failed checksum in the first loop
	fmt.Println("")
	for _, rf := range downloadFiles {

		fmt.Print("Downloading ", rf.Name, " ... ")

		makeDirError := os.MkdirAll(filepath.Dir(rf.Name), os.ModeDir)
		if makeDirError != nil {
			fmt.Println("Unable to create directory for ", rf.Name, " : ", makeDirError)
			downloadErrors++
			continue
		}

		fullURL := downloadRoot + rf.Name
		response, connectionError := http.Get(fullURL)
		if connectionError != nil {
			fmt.Println(connectionError)
			downloadErrors++
			continue
		}

		if response.StatusCode != 200 {
			fmt.Println("HTTP", response.StatusCode)
			downloadErrors++
			continue
		}

		// create file if doesn't exist, truncate any existing bytes
		downloadTarget, openError := os.OpenFile(rf.Name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if openError != nil {
			fmt.Println(openError)
			downloadErrors++
			continue
		}

		reader := bufio.NewReader(response.Body)
		_, writeError := reader.WriteTo(downloadTarget)
		if writeError == nil {
			// seek to beginning or the next CheckHash fails
			downloadTarget.Seek(0, os.SEEK_SET)

			if rf.CheckHash(downloadTarget) {
				fmt.Println("OK")
			} else {
				fmt.Println("Checksum failed")
				downloadErrors++
			}
		} else {
			fmt.Println(writeError)
			downloadErrors++
		}

		downloadTarget.Close()
		response.Body.Close()
	}
	fmt.Println("")

	if downloadErrors > 0 {
		fmt.Printf("Completed with %d errors\n", downloadErrors)
	} else {
		fmt.Println("Done :-)")
	}

	fmt.Println("")
	fmt.Println("Press Enter to close")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

func getRepositoryContent() (string, []repositoryFile) {
	var files []repositoryFile

	response, connectionError := http.Get(repoURL)
	if connectionError != nil {
		fmt.Println(connectionError)
		return "", nil
	}

	if response.StatusCode != 200 {
		fmt.Println("Unable to get repository data from", repoURL)
		fmt.Println("HTTP status code", response.StatusCode)
		return "", nil
	}

	repositoryBytes, readError := ioutil.ReadAll(response.Body)
	if readError != nil {
		fmt.Println(readError)
		return "", nil
	}

	data := repository{}
	json.Unmarshal(repositoryBytes, &data)

	for _, entry := range data.Files {
		if len(entry) != 2 {
			fmt.Println("Files entry does not contain 2 items")
			continue
		}
		newEntry := repositoryFile{
			Name: entry[0],
			Hash: entry[1],
		}
		files = append(files, newEntry)
	}
	return data.DownloadRoot, files
}

func calculateHash(f *os.File) string {
	hash := sha1.New()
	data := make([]byte, 1024*1024)

	for {
		_, readError := f.Read(data)
		if readError == io.EOF {
			break
		}
		hash.Write(data)
	}
	calculated := hash.Sum(nil)
	return hex.EncodeToString(calculated)
}
