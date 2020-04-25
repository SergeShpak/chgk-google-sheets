package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
	"google.golang.org/api/sheets/v4"
)

const (
	bucketGameConfiguration = "game-configuration"
	bucketTeamsSpreadsheets = "teams-spreadsheets"
	bucketGameResults       = "game-results"
)

const (
	bucketGameConfiguration_managerSpreadsheet = "manager-spreadsheet"
)

type boltManager struct {
	dbFile string
}

type storeSpreadsheet struct {
	ID  string
	URL string
}

func newStoreSpreadsheet(sheet *sheets.Spreadsheet) *storeSpreadsheet {
	if sheet == nil {
		return nil
	}
	s := &storeSpreadsheet{
		ID:  sheet.SpreadsheetId,
		URL: sheet.SpreadsheetUrl,
	}
	return s
}

type storeGameSpreadsheets struct {
	manager *storeSpreadsheet
	teams   map[string]*storeSpreadsheet
}

func newStoreGameSpreadsheets(sheets *gameSpreadsheets) *storeGameSpreadsheets {
	storeSheets := &storeGameSpreadsheets{}
	if sheets == nil {
		return storeSheets
	}
	if sheets.manager != nil {
		storeSheets.manager = newStoreSpreadsheet(sheets.manager)
	}
	storeSheets.teams = make(map[string]*storeSpreadsheet, len(sheets.teams))
	for team, sheet := range sheets.teams {
		storeSheets.teams[team] = newStoreSpreadsheet(sheet)
	}
	return storeSheets
}

func (s *storeGameSpreadsheets) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("manager: %s\n", s.manager.URL))
	for team, sheet := range s.teams {
		sb.WriteString(fmt.Sprintf("team %s: %s\n", team, sheet.URL))
	}
	return sb.String()
}

func (b *boltManager) saveSpreadsheets(req *storeGameSpreadsheets) error {
	err := b.update(func(tx *bolt.Tx) error {
		buckGameConfig, err := getBucket(tx, bucketGameConfiguration)
		if err != nil {
			return err
		}
		managerBytes, err := json.Marshal(req.manager)
		if err != nil {
			return err
		}
		if err := buckGameConfig.Put([]byte(bucketGameConfiguration_managerSpreadsheet), managerBytes); err != nil {
			return err
		}
		if len(req.teams) == 0 {
			return nil
		}
		buckTeamsSpreadsheets, err := getBucket(tx, bucketTeamsSpreadsheets)
		if err != nil {
			return err
		}
		for name, spreadsheet := range req.teams {
			spreadsheetBytes, err := json.Marshal(spreadsheet)
			if err != nil {
				return err
			}
			if err := buckTeamsSpreadsheets.Put([]byte(name), spreadsheetBytes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (b *boltManager) getSpreadsheets() (*storeGameSpreadsheets, error) {
	spreadsheets := &storeGameSpreadsheets{}
	err := b.read(func(tx *bolt.Tx) error {
		buckGameConfig, err := getBucket(tx, bucketGameConfiguration)
		if err != nil {
			return err
		}
		managerBytes := buckGameConfig.Get([]byte(bucketGameConfiguration_managerSpreadsheet))
		if err := json.Unmarshal(managerBytes, &spreadsheets.manager); err != nil {
			return err
		}
		buckTeamsSpreadsheets, err := getBucket(tx, bucketTeamsSpreadsheets)
		if err != nil {
			if _, ok := err.(*errorInexistantBucket); ok {
				return nil
			}
			return err
		}
		spreadsheets.teams = make(map[string]*storeSpreadsheet)
		err = buckTeamsSpreadsheets.ForEach(func(name, spreadsheet []byte) error {
			var teamStoreSheet storeSpreadsheet
			if err := json.Unmarshal(spreadsheet, &teamStoreSheet); err != nil {
				return err
			}
			spreadsheets.teams[string(name)] = &teamStoreSheet
			return nil
		})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return spreadsheets, nil
}

type ResponseStatus int

const (
	ResponseStatusOK ResponseStatus = iota + 1
	ResponseStatusKO
	ResponseStatusInQuestion
	ResponseStatusNotChecked
)

func (s ResponseStatus) String() string {
	switch s {
	case ResponseStatusOK:
		return "+"
	case ResponseStatusKO:
		return "-"
	case ResponseStatusInQuestion:
		return "?"
	case ResponseStatusNotChecked:
		return "{}"
	default:
		return fmt.Sprintf("unexpected status %d", s)
	}
}

type roundResponse struct {
	Response string
	Status   ResponseStatus
}

type roundResults struct {
	Round   int
	Results map[string]*roundResponse
}

func (r *roundResults) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Round %d results:\n", r.Round))
	for team, result := range r.Results {
		sb.WriteString(fmt.Sprintf("\t team %s: %s\t%v\n", team, result.Response, result.Status))
	}
	return sb.String()
}

func (b *boltManager) saveRoundResults(req *roundResults) error {
	err := b.update(func(tx *bolt.Tx) error {
		buckGameResults, err := getBucket(tx, bucketGameResults)
		if err != nil {
			return err
		}
		results, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if err := buckGameResults.Put([]byte(strconv.Itoa(req.Round)), results); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (b *boltManager) getRoundResults(round int) (*roundResults, error) {
	roundResults := &roundResults{}
	err := b.read(func(tx *bolt.Tx) error {
		buckGameResults, err := getBucket(tx, bucketGameResults)
		if err != nil {
			if _, ok := err.(*errorInexistantBucket); ok {
				return nil
			}
			return err
		}
		results := buckGameResults.Get([]byte(strconv.Itoa(round)))
		if len(results) == 0 {
			return fmt.Errorf("round %d results are not found", round)
		}
		if err := json.Unmarshal(results, roundResults); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return roundResults, nil
}

func (b *boltManager) update(fn func(tx *bolt.Tx) error) error {
	db, err := bolt.Open(b.dbFile, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		if err := createBuckets(tx); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (b *boltManager) read(fn func(tx *bolt.Tx) error) error {
	db, err := bolt.Open(b.dbFile, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	err = db.View(func(tx *bolt.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func createBuckets(tx *bolt.Tx) error {
	buckets := []string{bucketGameConfiguration, bucketTeamsSpreadsheets, bucketGameResults}
	for _, buck := range buckets {
		if _, err := tx.CreateBucketIfNotExists([]byte(buck)); err != nil {
			return err
		}
	}
	return nil
}

func getBucket(tx *bolt.Tx, buckName string) (*bolt.Bucket, error) {
	buck := tx.Bucket([]byte(buckName))
	if buck == nil {
		return nil, &errorInexistantBucket{bucket: buckName}
	}
	return buck, nil
}

type errorInexistantBucket struct {
	bucket string
}

func (e *errorInexistantBucket) Error() string {
	return fmt.Sprintf("bucket %s does not exist", e.bucket)
}
