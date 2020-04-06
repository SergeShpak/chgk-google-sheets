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
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type gameSpreadsheets struct {
	manager *sheets.Spreadsheet
	teams   map[string]*sheets.Spreadsheet
}

type app struct {
	config  *Config
	service *sheets.Service
	bolt    *boltManager
}

func newApp(config *Config) (*app, error) {
	if config == nil {
		return nil, fmt.Errorf("internal error: config passed to newApp cannot be nil")
	}
	if err := checkOutputDir(config.NewGame, config.OutputDir); err != nil {
		return nil, err
	}
	tok, oauthConfig, err := getOauth2Token(config.CredsFile, config.OutputDir)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	service, err := sheets.NewService(ctx, option.WithTokenSource(oauthConfig.TokenSource(ctx, tok)))
	if err != nil {
		return nil, err
	}
	dbFile := path.Join(config.OutputDir, "bolt-db")
	app := &app{
		config:  config,
		service: service,
		bolt: &boltManager{
			dbFile: dbFile,
		},
	}
	return app, nil
}

func (a *app) Run() error {
	if a.config.NewGame {
		_, err := a.CreateGameSpreadsheets()
		if err != nil {
			return err
		}
	} else {
		if err := a.DeleteGameSpreadsheets(); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) CreateGameSpreadsheets() (*gameSpreadsheets, error) {
	sheets := &gameSpreadsheets{}
	var err error
	sheets.manager, err = a.createManagerSpreadsheet()
	if err != nil {
		return nil, err
	}
	sheets.teams, err = a.createTeamsSpreadsheets()
	if err != nil {
		return nil, err
	}
	if err := a.bolt.saveSpreadsheets(newStoreGameSpreadsheets(sheets)); err != nil {
		return nil, err
	}
	if err := a.fillGameSheets(sheets); err != nil {
		return nil, err
	}
	return sheets, err
}

func (a *app) fillGameSheets(sheets *gameSpreadsheets) error {
	if err := a.fillManagerSpreadsheet(sheets.manager); err != nil {
		return err
	}
	for _, sheet := range sheets.teams {
		if err := a.fillTeamSpreadsheet(sheet); err != nil {
			return err
		}
	}
	if err := a.linkManagerTeams(sheets); err != nil {
		return err
	}
	return nil
}

func (a *app) linkManagerTeams(gameSheets *gameSpreadsheets) error {
	groups, err := a.createLinkManagerTeamsGroups(gameSheets)
	if err != nil {
		return err
	}
	valuesService := sheets.NewSpreadsheetsValuesService(a.service)
	_, err = valuesService.BatchUpdate(gameSheets.manager.SpreadsheetId, &sheets.BatchUpdateValuesRequest{
		ValueInputOption: "USER_ENTERED",
		Data:             groups,
	}).Do()
	if err != nil {
		return err
	}
	return nil
}

func (a *app) createLinkManagerTeamsGroups(gameSheets *gameSpreadsheets) ([]*sheets.ValueRange, error) {
	if len(a.config.Teams) == 0 || (a.config.NumberOfQuestions < 0 && !a.config.HasWarmUpQuestion) {
		return nil, nil
	}
	groups := make([]*sheets.ValueRange, 0)
	createGroupFn := func(length int) error {
		r, err := a.getLinkRange(len(groups), length)
		if err != nil {
			return err
		}
		values := make([][]interface{}, length)
		currTeamRow := 2 + 3*len(groups)
		for i := 0; i < length; i++ {
			currColumn := int('A') + i
			values[i] = make([]interface{}, len(a.config.Teams))
			for j := 0; j < len(a.config.Teams); j++ {
				values[i][j] = fmt.Sprintf("=IMPORTRANGE(\"%s\", \"Sheet1!%c%d\")", gameSheets.teams[a.config.Teams[j]].SpreadsheetUrl, rune(currColumn), currTeamRow)
			}
		}
		g := &sheets.ValueRange{
			MajorDimension: "COLUMNS",
			Range:          r,
			Values:         values,
		}
		groups = append(groups, g)
		return nil
	}
	if a.config.HasWarmUpQuestion {
		if err := createGroupFn(1); err != nil {
			return nil, err
		}
	}
	quot := a.config.NumberOfQuestions / 12
	for i := 0; i < quot; i++ {
		if err := createGroupFn(12); err != nil {
			return nil, err
		}
	}
	rem := a.config.NumberOfQuestions % 12
	if rem != 0 {
		if err := createGroupFn(rem); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func (a *app) getLinkRange(offset int, length int) (string, error) {
	if length > 24 {
		return "", fmt.Errorf("group length must be inferior to 25")
	}
	startRow := offset*(len(a.config.Teams)+2) + 2
	endRow := startRow + len(a.config.Teams)
	startColumn := int('B')
	endColumn := startColumn + length
	r := fmt.Sprintf("%c%d:%c%d", rune(startColumn), startRow, rune(endColumn), endRow)
	return r, nil
}

func (a *app) fillManagerSpreadsheet(manager *sheets.Spreadsheet) error {
	groups, err := a.createManagerAnswerGroups()
	if err != nil {
		return err
	}
	valuesService := sheets.NewSpreadsheetsValuesService(a.service)
	_, err = valuesService.BatchUpdate(manager.SpreadsheetId, &sheets.BatchUpdateValuesRequest{
		ValueInputOption: "USER_ENTERED",
		Data:             groups,
	}).Do()
	if err != nil {
		return err
	}
	return nil
}

func (a *app) fillTeamSpreadsheet(team *sheets.Spreadsheet) error {
	groups, err := a.createTeamAnswerGroups()
	if err != nil {
		return err
	}
	valuesService := sheets.NewSpreadsheetsValuesService(a.service)
	_, err = valuesService.BatchUpdate(team.SpreadsheetId, &sheets.BatchUpdateValuesRequest{
		ValueInputOption: "USER_ENTERED",
		Data:             groups,
	}).Do()
	if err != nil {
		return err
	}
	return nil
}

func (a *app) createTeamAnswerGroups() ([]*sheets.ValueRange, error) {
	if a.config.NumberOfQuestions < 0 && !a.config.HasWarmUpQuestion {
		return nil, nil
	}
	groups := make([]*sheets.ValueRange, 0)
	currQuestionIndex := -1
	createGroupFn := func(length int) error {
		r, err := a.getTeamRange(len(groups), length)
		if err != nil {
			return err
		}
		values := make([][]interface{}, 2)
		values[0] = make([]interface{}, length)
		for j := 0; j < length; j++ {
			values[0][j] = currQuestionIndex + j + 1
		}
		currQuestionIndex += length
		g := &sheets.ValueRange{
			MajorDimension: "ROWS",
			Range:          r,
			Values:         values,
		}
		groups = append(groups, g)
		return nil
	}
	if a.config.HasWarmUpQuestion {
		if err := createGroupFn(1); err != nil {
			return nil, err
		}
	}
	quot := a.config.NumberOfQuestions / 12
	for i := 0; i < quot; i++ {
		if err := createGroupFn(12); err != nil {
			return nil, err
		}
	}
	rem := a.config.NumberOfQuestions % 12
	if rem != 0 {
		if err := createGroupFn(rem); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func (a *app) getTeamRange(offset int, length int) (string, error) {
	if length > 25 {
		return "", fmt.Errorf("group length must be inferior to 25")
	}
	startRow := offset*3 + 1
	endRow := startRow + 1
	startColumn := int('A')
	endColumn := startColumn + length
	r := fmt.Sprintf("%c%d:%c%d", rune(startColumn), startRow, rune(endColumn), endRow)
	return r, nil
}

func (a *app) createManagerAnswerGroups() ([]*sheets.ValueRange, error) {
	if len(a.config.Teams) == 0 || (a.config.NumberOfQuestions < 0 && !a.config.HasWarmUpQuestion) {
		return nil, nil
	}
	teamsCol := make([]interface{}, len(a.config.Teams)+1)
	teamsCol[0] = "Teams"
	for i, team := range a.config.Teams {
		teamsCol[i+1] = team
	}
	groups := make([]*sheets.ValueRange, 0)
	currQuestionIndex := -1
	createGroupFn := func(length int) error {
		r, err := a.getManagerRange(len(groups), length)
		if err != nil {
			return err
		}
		values := make([][]interface{}, length+1)
		values[0] = teamsCol
		for j := 1; j < length+1; j++ {
			values[j] = []interface{}{currQuestionIndex + j}
		}
		currQuestionIndex += length
		g := &sheets.ValueRange{
			MajorDimension: "COLUMNS",
			Range:          r,
			Values:         values,
		}
		groups = append(groups, g)
		return nil
	}
	if a.config.HasWarmUpQuestion {
		if err := createGroupFn(1); err != nil {
			return nil, err
		}
	}
	quot := a.config.NumberOfQuestions / 12
	for i := 0; i < quot; i++ {
		if err := createGroupFn(12); err != nil {
			return nil, err
		}
	}
	rem := a.config.NumberOfQuestions % 12
	if rem != 0 {
		if err := createGroupFn(rem); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func (a *app) getManagerRange(offset int, length int) (string, error) {
	if length > 25 {
		return "", fmt.Errorf("group length must be inferior to 25")
	}
	startRow := offset*(len(a.config.Teams)+2) + 1
	endRow := startRow + len(a.config.Teams) + 1
	startColumn := int('A')
	endColumn := startColumn + length
	r := fmt.Sprintf("%c%d:%c%d", rune(startColumn), startRow, rune(endColumn), endRow)
	return r, nil
}

func (a *app) DeleteGameSpreadsheets() error {
	storeSheets, err := a.bolt.getSpreadsheets()
	if err != nil {
		return err
	}
	if storeSheets.manager != nil {
		if err := a.deleteSpreadsheet(storeSheets.manager.ID); err != nil {
			return fmt.Errorf("failed to delete the manager spreadsheet %s: %v", storeSheets.manager.URL, err)
		}
		log.Println("deleted manager spreadsheet")
	}
	for team, sheet := range storeSheets.teams {
		if err := a.deleteSpreadsheet(sheet.ID); err != nil {
			return fmt.Errorf("failed to delete the team %s spreadsheet %s: %v", team, sheet.URL, err)
		}
		log.Printf("deleted team %s spreadsheet", team)
	}
	return nil
}

func (a *app) deleteSpreadsheet(sheetID string) error {
	_, err := a.service.Spreadsheets.BatchUpdate(sheetID, &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			&sheets.Request{
				DeleteSheet: &sheets.DeleteSheetRequest{
					SheetId: 0,
				},
			},
		},
	}).Do()
	if err != nil {
		return err
	}
	return nil
}

func (a *app) createManagerSpreadsheet() (*sheets.Spreadsheet, error) {
	sheet := &sheets.Spreadsheet{
		Properties: &sheets.SpreadsheetProperties{
			Title: fmt.Sprintf("%s-manager", a.config.GameName),
		},
	}
	createdSpreadsheet, err := a.service.Spreadsheets.Create(sheet).Do()
	if err != nil {
		return nil, err
	}
	log.Printf("created the manager spreadsheet: %s", createdSpreadsheet.SpreadsheetUrl)
	return createdSpreadsheet, err
}

func (a *app) createTeamsSpreadsheets() (map[string]*sheets.Spreadsheet, error) {
	teamsSpreadsheets := make(map[string]*sheets.Spreadsheet, len(a.config.Teams))
	for _, team := range a.config.Teams {
		sheet := &sheets.Spreadsheet{
			Properties: &sheets.SpreadsheetProperties{
				Title: fmt.Sprintf("%s-team-%s", a.config.GameName, team),
			},
		}
		createdSpreadsheet, err := a.service.Spreadsheets.Create(sheet).Do()
		if err != nil {
			return teamsSpreadsheets, err
		}
		log.Printf("created the team %s spreadsheet: %s", team, createdSpreadsheet.SpreadsheetUrl)
		teamsSpreadsheets[team] = createdSpreadsheet
	}
	return teamsSpreadsheets, nil
}

func getOauth2Token(credsFile string, outputDir string) (*oauth2.Token, *oauth2.Config, error) {
	b, err := ioutil.ReadFile(credsFile)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to read google sheets API credentials file %s: %v", credsFile, err)
	}
	// If modifying these scopes, delete your previously saved token.json.
	oauth2Config, err := google.ConfigFromJSON(b, "https://www.googleapis.com/auth/spreadsheets")
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse client secret file %s to oauth2 config: %v", credsFile, err)
	}
	gameFiles, err := ioutil.ReadDir(outputDir)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to read the game dir %s: %v", outputDir, err)
	}
	for _, f := range gameFiles {
		if f.Name() != "secret-token" {
			continue
		}
		tok, err := getTokenFromFile(path.Join(outputDir, f.Name()))
		if err != nil {
			return nil, nil, err
		}
		return tok, oauth2Config, nil
	}
	tok, err := getTokenFromWeb(oauth2Config)
	if err != nil {
		return nil, nil, err
	}
	if err := saveGameToken(outputDir, tok); err != nil {
		return nil, nil, err
	}
	return tok, oauth2Config, nil
}

func getTokenFromFile(file string) (*oauth2.Token, error) {
	tokenFile, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("unable to read token file %s: %v", file, err)
	}
	defer tokenFile.Close()
	tok := oauth2.Token{}
	if err := json.NewDecoder(tokenFile).Decode(&tok); err != nil {
		return nil, fmt.Errorf("failed to decode the token file %s: %v", file, err)
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
