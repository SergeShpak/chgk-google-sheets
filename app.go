package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"strconv"
	"strings"

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
	}
	for {
		fmt.Print("Enter command: ")
		reader := bufio.NewReader(os.Stdin)
		cmdStr, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to scan the command: %v", err)
		}
		cmdStr = cmdStr[:len(cmdStr)-1]
		fmt.Println()
		cmdErr := func(cmd string, err error) error {
			return fmt.Errorf("command \"%s\" failed: %v", cmd, err)
		}
		switch cmd := getCommand(cmdStr); cmd {
		case "listURLs":
			if err := a.CmdListURLs(); err != nil {
				return cmdErr(cmdStr, err)
			}
		case "fetch":
			if err := a.CmdFetchResults(cmdStr); err != nil {
				return cmdErr(cmdStr, err)
			}
		case "get":
			if err := a.CmdGetResults(cmdStr); err != nil {
				return cmdErr(cmdStr, err)
			}
		case "check":
			if err := a.CmdCheckResults(cmdStr); err != nil {
				return cmdErr(cmdStr, err)
			}
		case "total":
			if err := a.CmdGetTotal(); err != nil {
				return cmdErr(cmdStr, err)
			}
		case "exit":
			return nil
		default:
			if len(cmd) == 0 {
				fmt.Printf("got an empty command\n")
				continue
			}
			fmt.Printf("unknown command: %s\n", cmd)
			continue
		}
	}
}

func (a *app) CmdListURLs() error {
	sheets, err := a.GetGameSpreadsheets()
	if err != nil {
		return err
	}
	fmt.Println(sheets)
	return nil
}

func (a *app) CmdGetTotal() error {
	var firstInd int
	if a.config.HasWarmUpQuestion {
		firstInd = 1
	}
	total := make(map[string]int)
	for _, team := range a.config.Teams {
		total[team] = 0
	}
	for i := firstInd; i < a.config.NumberOfQuestions; i++ {
		results, err := a.bolt.getRoundResults(i)
		if err != nil {
			if err.Error() == fmt.Sprintf("round %d results are not found", i) {
				continue
			}
			return err
		}
		for team, res := range results.Results {
			if _, ok := total[team]; !ok {
				return fmt.Errorf("team %s is unknown", team)
			}
			if res.Status == ResponseStatusOK {
				total[team]++
			}
		}
	}
	for team, count := range total {
		fmt.Printf("Team %s: %d\n", team, count)
	}
	return nil
}

func (a *app) CmdFetchResults(cmdStr string) error {
	round, err := getRoundNumber(cmdStr)
	if err != nil {
		return fmt.Errorf("failed to parse fetchResp request: %v", err)
	}
	results, err := a.fetchRoundResults(round)
	if err != nil {
		return fmt.Errorf("failed to fetch round results: %v", err)
	}
	resultsToStore := make(map[string]*roundResponse)
	for team, resp := range results {
		resultsToStore[team] = &roundResponse{
			Response: resp,
			Status:   ResponseStatusNotChecked,
		}
	}
	storeReq := &roundResults{
		Round:   round,
		Results: resultsToStore,
	}
	if err := a.bolt.saveRoundResults(storeReq); err != nil {
		return fmt.Errorf("failed to store round results: %v", err)
	}
	fmt.Println(storeReq)
	return nil
}

//TODO: refactor as two calls: to get round results and to store round results
func (a *app) CmdCheckResults(cmdStr string) error {
	round, err := getRoundNumber(cmdStr)
	if err != nil {
		return fmt.Errorf("failed to parse check request: %v", err)
	}
	results, err := a.bolt.getRoundResults(round)
	if err != nil {
		return err
	}
	if err := checkResults(results); err != nil {
		return err
	}
	if err := a.bolt.saveRoundResults(results); err != nil {
		return fmt.Errorf("failed to store round results: %v", err)
	}
	return nil
}

func checkResults(results *roundResults) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("Checking results for the round %d\n", results.Round)
	for team, result := range results.Results {
		fmt.Printf("Team %s, response: %s, previous status: %v\n", team, result.Response, result.Status)
		for {
			statusStr, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("failed to scan the command: %v", err)
			}
			statusStr = statusStr[:len(statusStr)-1]

			switch statusStr {
			case "+":
				results.Results[team].Status = ResponseStatusOK
			case "-":
				results.Results[team].Status = ResponseStatusKO
			case "?":
				results.Results[team].Status = ResponseStatusInQuestion
			case "":
				results.Results[team].Status = ResponseStatusNotChecked
			default:
				fmt.Println("Unknown status, try again")
				continue
			}
			break
		}
	}
	return nil
}

func (a *app) CmdGetResults(cmdStr string) error {
	round, err := getRoundNumber(cmdStr)
	if err != nil {
		return fmt.Errorf("failed to parse fetch request: %v", err)
	}
	roundResults, err := a.bolt.getRoundResults(round)
	if err != nil {
		return err
	}
	fmt.Println(roundResults)
	return nil
}

func getRoundNumber(cmdStr string) (int, error) {
	sSplitted := strings.Split(cmdStr, " ")
	if len(sSplitted) != 2 {
		return 0, fmt.Errorf("expected 1 argument, got %d", len(sSplitted)-1)
	}
	roundNumberStr := sSplitted[1]
	round64, err := strconv.ParseInt(roundNumberStr, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("failed to parse argument %s as a round number: %v", roundNumberStr, err)
	}
	return int(round64), nil
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

func (a *app) GetGameSpreadsheets() (*storeGameSpreadsheets, error) {
	spreadsheets, err := a.bolt.getSpreadsheets()
	if err != nil {
		return nil, err
	}
	return spreadsheets, nil
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
	ranges, err := a.getTeamAnswerGridRanges()
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
	updateBordersRequests := make([]*sheets.Request, len(ranges))
	border := &sheets.Border{
		Style: "SOLID",
	}
	for i, r := range ranges {
		updateBordersRequests[i] = &sheets.Request{
			UpdateBorders: &sheets.UpdateBordersRequest{
				Range:  r,
				Bottom: border,
				Top:    border,
				Left:   border,
				Right:  border,
			},
		}
	}
	spreadsheetsService := sheets.NewSpreadsheetsService(a.service)
	_, err = spreadsheetsService.BatchUpdate(team.SpreadsheetId, &sheets.BatchUpdateSpreadsheetRequest{
		Requests: updateBordersRequests,
	}).Do()
	return nil
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
	groups, err := a.createGroups(func(length int, currQuestionIndex int, groups []*sheets.ValueRange) ([]*sheets.ValueRange, error) {
		r, err := a.getManagerRange(len(groups), length)
		if err != nil {
			return nil, err
		}
		values := make([][]interface{}, length+1)
		values[0] = teamsCol
		for j := 1; j < length+1; j++ {
			values[j] = []interface{}{currQuestionIndex + j}
		}
		g := &sheets.ValueRange{
			MajorDimension: "COLUMNS",
			Range:          r,
			Values:         values,
		}
		groups = append(groups, g)
		return groups, nil
	})
	if err != nil {
		return nil, err
	}
	return groups, nil
}

func (a *app) createLinkManagerTeamsGroups(gameSheets *gameSpreadsheets) ([]*sheets.ValueRange, error) {
	if len(a.config.Teams) == 0 || (a.config.NumberOfQuestions < 0 && !a.config.HasWarmUpQuestion) {
		return nil, nil
	}
	groups, err := a.createGroups(func(length int, currQuestionIndex int, groups []*sheets.ValueRange) ([]*sheets.ValueRange, error) {
		r, err := a.getLinkRange(len(groups), length)
		if err != nil {
			return nil, err
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
		return groups, nil
	})
	if err != nil {
		return nil, err
	}
	return groups, nil
}

func (a *app) createTeamAnswerGroups() ([]*sheets.ValueRange, error) {
	if a.config.NumberOfQuestions < 0 && !a.config.HasWarmUpQuestion {
		return nil, nil
	}
	groups, err := a.createGroups(func(length int, currQuestionIndex int, groups []*sheets.ValueRange) ([]*sheets.ValueRange, error) {
		r, err := a.getTeamRange(len(groups), length)
		if err != nil {
			return nil, err
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
		return groups, nil
	})
	if err != nil {
		return nil, err
	}
	return groups, nil
}

func (a *app) getTeamAnswerGridRanges() ([]*sheets.GridRange, error) {
	if a.config.NumberOfQuestions < 0 && !a.config.HasWarmUpQuestion {
		return nil, nil
	}
	questionsGroupLength := 12
	questionGroupsCount := a.config.NumberOfQuestions / questionsGroupLength
	if a.config.NumberOfQuestions%questionsGroupLength != 0 {
		questionGroupsCount++
	}
	rangesCount := questionGroupsCount
	if a.config.HasWarmUpQuestion {
		rangesCount++
	}
	ranges := make([]*sheets.GridRange, 0, rangesCount)
	rowOffset := 0
	gapWidth := 1
	groupWidth := 2
	if a.config.HasWarmUpQuestion {
		warmupGridRange := &sheets.GridRange{
			StartColumnIndex: 0,
			EndColumnIndex:   1,
			StartRowIndex:    0,
			EndRowIndex:      2,
		}
		ranges = append(ranges, warmupGridRange)
		rowOffset += gapWidth + groupWidth
	}
	for i := 0; i < questionGroupsCount-1; i++ {
		r := &sheets.GridRange{
			StartColumnIndex: 0,
			EndColumnIndex:   int64(questionsGroupLength),
			StartRowIndex:    int64(rowOffset),
			EndRowIndex:      int64(rowOffset + groupWidth),
		}
		ranges = append(ranges, r)
		rowOffset += gapWidth + groupWidth
	}
	lastRangeLen := questionsGroupLength
	if a.config.NumberOfQuestions%questionsGroupLength != 0 {
		lastRangeLen = a.config.NumberOfQuestions % questionsGroupLength
	}
	r := &sheets.GridRange{
		StartColumnIndex: 0,
		EndColumnIndex:   int64(lastRangeLen),
		StartRowIndex:    int64(rowOffset),
		EndRowIndex:      int64(rowOffset + groupWidth),
	}
	ranges = append(ranges, r)
	return ranges, nil
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

func (a *app) createGroups(createGroupFn func(length int, currQuestionIndex int, groups []*sheets.ValueRange) ([]*sheets.ValueRange, error)) ([]*sheets.ValueRange, error) {
	groups := make([]*sheets.ValueRange, 0)
	var err error
	currQuestionIndex := -1
	if a.config.HasWarmUpQuestion {
		if groups, err = createGroupFn(1, currQuestionIndex, groups); err != nil {
			return nil, err
		}
	}
	currQuestionIndex++
	quot := a.config.NumberOfQuestions / 12
	for i := 0; i < quot; i++ {
		if groups, err = createGroupFn(12, currQuestionIndex, groups); err != nil {
			return nil, err
		}
		currQuestionIndex += 12
	}
	rem := a.config.NumberOfQuestions % 12
	if rem != 0 {
		if groups, err = createGroupFn(rem, currQuestionIndex, groups); err != nil {
			return nil, err
		}
	}
	currQuestionIndex += rem
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
				Title: fmt.Sprintf("%s: команда %s", a.config.GameName, team),
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

func (a *app) fetchRoundResults(round int) (map[string]string, error) {
	gameSpreadsheets, err := a.GetGameSpreadsheets()
	if err != nil {
		return nil, err
	}
	roundRange, err := a.getRoundRange(round)
	if err != nil {
		return nil, err
	}
	valuesService := sheets.NewSpreadsheetsValuesService(a.service)
	resp, err := valuesService.BatchGetByDataFilter(gameSpreadsheets.manager.ID, &sheets.BatchGetValuesByDataFilterRequest{
		DataFilters: []*sheets.DataFilter{
			&sheets.DataFilter{
				GridRange: roundRange,
			},
		},
		MajorDimension: "COLUMNS",
	}).Do()
	if err != nil {
		return nil, err
	}
	if len(resp.ValueRanges) != 1 {
		return nil, fmt.Errorf("unexpected response value range length: %d", len(resp.ValueRanges))
	}
	log.Println(resp.ValueRanges[0].ValueRange)
	if len(resp.ValueRanges[0].ValueRange.Values) != 1 {
		return nil, fmt.Errorf("unexpected length of ValueRange values: %d", len(resp.ValueRanges[0].ValueRange.Values))
	}
	resultsIface := resp.ValueRanges[0].ValueRange.Values[0]
	results := make(map[string]string, len(resultsIface))
	for i, r := range resultsIface {
		rStr, ok := r.(string)
		if !ok {
			return nil, fmt.Errorf("received value %v could not be cast to string", r)
		}
		results[a.config.Teams[i]] = rStr
	}
	return results, nil
}

func (a *app) getRoundRange(round int) (*sheets.GridRange, error) {
	if round < 0 || round >= a.config.NumberOfQuestions {
		return nil, fmt.Errorf("round %d is out of range [0; %d]", round, a.config.NumberOfQuestions)
	}
	if round == 0 {
		if !a.config.HasWarmUpQuestion {
			return nil, fmt.Errorf("round %d is invalid as the game does not have a warm-up question", round)
		}
		gr := &sheets.GridRange{
			StartRowIndex:    1,
			EndRowIndex:      int64(len(a.config.Teams)) + 1,
			StartColumnIndex: 1,
			EndColumnIndex:   2,
		}
		log.Printf("getting the grid range: %+v\n", gr)
		return gr, nil
	}
	groupWidth := 1 + len(a.config.Teams)
	gapWidth := 1
	firstGroupRow := 0
	if a.config.HasWarmUpQuestion {
		firstGroupRow += groupWidth + gapWidth
	}
	questionsCountInGroup := 12
	groupIndex := round / questionsCountInGroup
	groupRow := firstGroupRow + groupIndex*(groupWidth+gapWidth)
	firstResultRow := groupRow + 1
	lastResultRow := groupRow + len(a.config.Teams)
	questionMod := round % questionsCountInGroup
	if questionMod == 0 {
		questionMod = questionsCountInGroup
	}
	gr := &sheets.GridRange{
		StartRowIndex:    int64(firstResultRow),
		EndRowIndex:      int64(lastResultRow + 1),
		StartColumnIndex: int64(questionMod),
		EndColumnIndex:   int64(questionMod + 1),
	}
	return gr, nil
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

func getCommand(s string) string {
	sSplitted := strings.Split(s, " ")
	if len(sSplitted) == 0 {
		return ""
	}
	return sSplitted[0]
}
