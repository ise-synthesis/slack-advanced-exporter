package cmd

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	attachmentsApiToken string
)

var fetchAttachmentsCmd = &cobra.Command{
	Use:   "fetch-attachments",
	Short: "Fetch all file attachments and add them to the output archive",
	RunE:  fetchAttachments,
}

func init() {
	fetchAttachmentsCmd.PersistentFlags().StringVar(&attachmentsApiToken, "api-token", "", "Slack API token. Can be obtained here: https://api.slack.com/docs/oauth-test-tokens")
}

func fetchAttachments(cmd *cobra.Command, args []string) error {
	// Open the input archive.
	r, err := zip.OpenReader(inputArchive)
	if err != nil {
		fmt.Printf("Could not open input archive for reading: %s\n", inputArchive)
		os.Exit(1)
	}
	defer r.Close()

	// Run through all the files in the input archive.
	for _, file := range r.File {
		verbosePrintln(fmt.Sprintf("Processing file: %s\n", file.Name))

		if file.FileInfo().IsDir() {
			err = os.MkdirAll(file.Name, 0755)
			if err != nil {
				fmt.Printf("Failed to create dir: %s\n%s\n", file.Name, err)
				os.Exit(1)
			}
			continue
		}

		// Open the file from the input archive.
		inReader, err := file.Open()
		if err != nil {
			fmt.Printf("Failed to open file in input archive: %s\n\n%s", file.Name, err)
			os.Exit(1)
		}

		// Read the file into a byte array.
		inBuf, err := ioutil.ReadAll(inReader)
		if err != nil {
			fmt.Printf("Failed to read file in input archive: %s\n\n%s", file.Name, err)
		}

		outFile, err := os.Create(file.Name)
		if err != nil {
			fmt.Printf("Failed to create file: %s\n%s\n", file.Name, err)
			os.Exit(1)
		}
		_, err = outFile.Write(inBuf)
		if err != nil {
			fmt.Printf("Failed to write file: %s\n%s\n", file.Name, err)
			os.Exit(1)
		}

		outFile.Close()

		// Check if the file name matches the pattern for files we need to parse.
		splits := strings.Split(file.Name, "/")
		if len(splits) == 2 && !strings.HasPrefix(splits[0], "__") && strings.HasSuffix(splits[1], ".json") {
			// Parse this file.
			err = processChannelFile(file, inBuf, attachmentsApiToken)
			if err != nil {
				fmt.Printf("%s", err)
				os.Exit(1)
			}
		}
	}

	return nil
}

func processChannelFile(file *zip.File, inBuf []byte, token string) error {
	verbosePrintln("This is a 'channels' file. Examining it's contents for attachments.")

	// Parse the JSON of the file.
	var posts []SlackPost
	if err := json.Unmarshal(inBuf, &posts); err != nil {
		return errors.New("Couldn't parse the JSON file: " + file.Name + "\n\n" + err.Error() + "\n")
	}

	// Loop through all the posts.
	for _, post := range posts {
		// Support for legacy file_share posts.
		if post.Subtype == "file_share" {
			// Check there's a File property.
			if post.File == nil {
				log.Print("++++++ file_share post has no File property: " + post.Ts + "\n")
				continue
			}

			// Add the file as a single item in the array of the post's files.
			post.Files = []*SlackFile{post.File}
		}

		// If the post doesn't contain any files, move on.
		if post.Files == nil {
			continue
		}

		client := &http.Client{}

		// Loop through all the files.
		for _, file := range post.Files {
			// Check there's an Id, Name and either UrlPrivateDownload or UrlPrivate property.
			if len(file.Id) < 1 || len(file.Name) < 1 || !(len(file.UrlPrivate) > 0 || len(file.UrlPrivateDownload) > 0) {
				log.Print("++++++ file_share post has missing properties on its File object: " + post.Ts + "\n")
				continue
			}

			// Figure out the download URL to use.
			var downloadUrl string
			if len(file.UrlPrivateDownload) > 0 {
				downloadUrl = file.UrlPrivateDownload
			} else {
				downloadUrl = file.UrlPrivate
			}

			// Build the output file path.
			outputPath := "__uploads/" + file.Id + "/" + file.Name

			_, err := os.Stat(outputPath)
			if err == nil {
				log.Printf("Skip: %s\n", outputPath)
				continue
			}

			verbosePrintln(fmt.Sprintf("Downloading file %s (%s)", file.Id, file.Name))

			// Fetch the file.
			req, err := http.NewRequest("GET", downloadUrl, nil)
			if err != nil {
				log.Print("++++++ Failed to create file download request: " + downloadUrl)
				continue
			}
			if token != "" {
				req.Header.Add("Authorization", "Bearer "+token)
			}
			response, err := client.Do(req)
			if err != nil {
				log.Print("++++++ Failed to download the file: " + downloadUrl)
				continue
			}
			defer response.Body.Close()

			// Save the file to the output zip file.
			err = os.MkdirAll(filepath.Dir(outputPath), 0755)
			if err != nil {
				log.Printf("Failed to create dir: %s\n%s\n", filepath.Dir(outputPath), err)
				os.Exit(1)
			}
			outFile, err := os.Create(outputPath)
			if err != nil {
				log.Printf("Failed to create file: %s\n%s\n", outputPath, err)
				os.Exit(1)
			}
			_, err = io.Copy(outFile, response.Body)
			if err != nil {
				log.Print("++++++ Failed to write the downloaded file to the output archive: " + downloadUrl + "\n\n" + err.Error() + "\n")
				os.Exit(1)
			}
			outFile.Close()

			// Success at last.
			fmt.Printf("Downloaded attachment: %s.\n", outputPath)
		}
	}

	return nil
}
