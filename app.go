package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type app struct {
	config *Config
	token  *oauth2.Token
}

func newApp(config *Config) (*app, error) {
	if config == nil {
		return nil, fmt.Errorf("internal error: config passed to newApp cannot be nil")
	}
	if err := checkOutputDir(config.NewGame, config.OutputDir); err != nil {
		return nil, err
	}
	tok, err := getOauth2Token(config.CredsFile, config.OutputDir)
	if err != nil {
		return nil, err
	}
	app := &app{
		config: config,
		token:  tok,
	}
	return app, nil
}

func getOauth2Token(credsFile string, outputDir string) (*oauth2.Token, error) {
	gameFiles, err := ioutil.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("unable to read the game dir %s: %v", outputDir, err)
	}
	for _, f := range gameFiles {
		tok, err := getTokenFromFile(path.Join(outputDir, f.Name()))
		if err != nil {
			return nil, err
		}
		return tok, nil
	}
	b, err := ioutil.ReadFile(credsFile)
	if err != nil {
		return nil, fmt.Errorf("unable to read google sheets API credentials file %s: %v", credsFile, err)
	}
	// If modifying these scopes, delete your previously saved token.json.
	oauth2Config, err := google.ConfigFromJSON(b, "https://www.googleapis.com/auth/spreadsheets")
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file %s to oauth2 config: %v", credsFile, err)
	}
	tok, err := getTokenFromWeb(oauth2Config)
	if err != nil {
		return nil, err
	}
	if err := saveGameToken(outputDir, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func getTokenFromFile(file string) (*oauth2.Token, error) {
	tokenFile, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("unable to read token file %s: %v", file, err)
	}
	defer tokenFile.Close()
	tok := oauth2.Token{}
	if err := json.NewDecoder(tokenFile).Decode(&tok); err != nil {
		return nil, fmt.Errorf("failed to decode the token file %s: %v", tokenFile, err)
	}
	return &tok, nil
}

func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read the authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %v", err)
	}
	return tok, nil
}

func saveGameToken(outputDir string, token *oauth2.Token) error {
	tokFile := path.Join(outputDir, "secret-token")
	f, err := os.OpenFile(tokFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(token); err != nil {
		return fmt.Errorf("unable to same the game token to %s: %v", tokFile, err)
	}
	return nil
}

func checkOutputDir(isNewGame bool, outputDir string) error {
	if !isNewGame {
		return nil
	}
	files, err := ioutil.ReadDir(outputDir)
	if err != nil {
		if pErr, ok := err.(*os.PathError); ok {
			if pErr.Op == "open" && pErr.Path == outputDir && pErr.Err.Error() == "no such file or directory" {
				if err := os.MkdirAll(outputDir, 0755); err != nil {
					return fmt.Errorf("failed to create a new game directory %s: %v", outputDir, err)
				}
				return nil
			}
		}
		return err
	}
	for _, f := range files {
		fmt.Println(f.Name())
		if f.Name() == "secret-token" {
			continue
		}
		return fmt.Errorf("cannot use a non-empty output directory %s to create a game", outputDir)
	}
	return nil
}
