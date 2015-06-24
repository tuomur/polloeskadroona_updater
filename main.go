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

var repoUrl = "http://koti.kapsi.fi/darkon/polloeskadroona/repo/updater.json"

type repository struct {
	DownloadRoot string
	Files        [][]string
}

type repositoryFile struct {
	Name string
	Hash string
}

func (f repositoryFile) HasValidPath() bool {
	cpath, _ := os.Getwd()
	abs_path, err := filepath.Abs(f.Name)
	if err != nil {
		fmt.Println("Unable to resolve path for", f.Name)
		return false
	}
	cleaned := path.Clean(abs_path)
	return strings.Contains(cleaned, cpath)
}

func (f repositoryFile) CheckHash(i *os.File) bool {
	calculatedHash := calculateHash(i)
	return calculatedHash == f.Hash
}

func main() {
	var cRepoUrl = flag.String("repoUrl", "", "Set URL to custom repository json")
	var dirName = flag.String("createRepo", "", "Directory to create a repository json from")
	var outputName = flag.String("output", "updater.json", "Name of the json file for -createRepo")

	flag.Parse()

	if len(*cRepoUrl) > 0 {
		repoUrl = *cRepoUrl
	}

	if len(*dirName) == 0 {
		updateFiles()
	} else {
		createRepo(*dirName, *outputName)
	}
}

func createRepo(dirName string, outputName string) {
	newRepo := repository{}
	newRepo.DownloadRoot = "http://koti.kapsi.fi/darkon/polloeskadroona/repo/"
	filepath.Walk(dirName, func(wpath string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}

		f, open_err := os.Open(wpath)
		if open_err != nil {
			return open_err
		}

		hash := calculateHash(f)
		uPath := filepath.ToSlash(wpath)
		fmt.Println(uPath, ":", hash)
		newRepo.Files = append(newRepo.Files, []string{uPath, hash})
		return nil
	})

	json_content, m_err := json.Marshal(newRepo)
	if m_err != nil {
		fmt.Println(m_err)
		return
	}
	ioutil.WriteFile(outputName, json_content, 0644)
	fmt.Println("\nWriting outout to", outputName)
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
	fmt.Println("Repository:", repoUrl)

	downloadRoot, files := getrepositoryContent()
	if files == nil {
		return
	}

	downloadFiles := make([]repositoryFile, 0)
	downloadErrors := 0

	directoriesToPrune := make([]string, 0)

	fmt.Println("")

	// check existing files and their checksum
	for _, fi := range files {

		if !fi.HasValidPath() {
			// invalid path, ignore
			continue
		}

		parts := strings.Split(fi.Name, "/")
		if !stringInSlice(parts[0], directoriesToPrune) {
			directoriesToPrune = append(directoriesToPrune, parts[0])
		}

		f, err := os.Open(fi.Name)

		if err != nil {
			fmt.Println(fi.Name, ": Download")
			downloadFiles = append(downloadFiles, fi)
			continue
		}
		defer f.Close()

		var status string
		fmt.Printf(fi.Name + " : ")

		if fi.CheckHash(f) {
			status = "OK"
		} else {
			status = "Download (Changed)"
			downloadFiles = append(downloadFiles, fi)
		}

		fmt.Println(status)
	}

	// remove any file that is not part of the repository. directories will
	// not be removed
	fmt.Println("")
	fmt.Println("Pruning non-repository files")
	for _, pruneDir := range directoriesToPrune {
		if _, err := os.Stat(pruneDir); os.IsNotExist(err) {
			continue
		}
		filepath.Walk(pruneDir, func(wpath string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}
			uPath := filepath.ToSlash(wpath)
			belongsToRepo := false
			for _, fi := range files {
				if uPath == fi.Name {
					belongsToRepo = true
				}
			}
			if !belongsToRepo {
				fmt.Println("Removing", uPath)
				remove_err := os.RemoveAll(uPath)
				if remove_err != nil {
					return remove_err
				}
			}
			return nil
		})
	}

	// download files that are missing or failed checksum in the first loop
	fmt.Println("")
	for _, rf := range downloadFiles {

		fmt.Print("Downloading ", rf.Name, " ... ")

		mkdir_err := os.MkdirAll(filepath.Dir(rf.Name), os.ModeDir)
		if mkdir_err != nil {
			fmt.Println("Unable to create directory for ", rf.Name, " : ", mkdir_err)
			downloadErrors += 1
			continue
		}

		fullUrl := downloadRoot + rf.Name
		response, get_err := http.Get(fullUrl)
		if get_err != nil {
			fmt.Println(get_err)
			downloadErrors += 1
			continue
		}

		if response.StatusCode != 200 {
			fmt.Println("HTTP", response.StatusCode)
			downloadErrors += 1
			continue
		}

		dl, err := os.OpenFile(rf.Name, os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			fmt.Println(err)
			downloadErrors += 1
			continue
		}
		defer dl.Close()

		r := bufio.NewReader(response.Body)
		r.WriteTo(dl)

		// seek to beginning or the next CheckHash fails
		dl.Seek(0, 0)

		if rf.CheckHash(dl) {
			fmt.Println("OK")
		} else {
			fmt.Println("Checksum failed")
			downloadErrors += 1
		}
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

func getrepositoryContent() (string, []repositoryFile) {
	var files []repositoryFile

	response, get_err := http.Get(repoUrl)
	if get_err != nil {
		fmt.Println(get_err)
		return "", nil
	}

	if response.StatusCode != 200 {
		fmt.Println("Unable to get repository data from", repoUrl)
		fmt.Println("HTTP status code", response.StatusCode)
		return "", nil
	}

	repo_bytes, read_err := ioutil.ReadAll(response.Body)
	if read_err != nil {
		fmt.Println(read_err)
		return "", nil
	}

	data := repository{}
	json.Unmarshal(repo_bytes, &data)

	for _, v := range data.Files {
		if len(v) != 2 {
			fmt.Println("Files entry does not contain 2 items")
			continue
		}
		f := repositoryFile{
			Name: v[0],
			Hash: v[1],
		}
		files = append(files, f)
	}
	return data.DownloadRoot, files
}

func calculateHash(f *os.File) string {
	hash := sha1.New()
	data := make([]byte, 1024*1024)

	for {
		_, err := f.Read(data)
		if err == io.EOF {
			break
		}
		hash.Write(data)
	}
	calculated := hash.Sum(nil)
	return hex.EncodeToString(calculated)
}
