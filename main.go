package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	parsedFlags, err := parseFlags()
	if err != nil {
		log.Printf("[ERR]: %v", err)
		flag.PrintDefaults()
		os.Exit(1)
	}
	conf, err := getConfiguration(parsedFlags)
	if err != nil {
		log.Fatalf("[ERR]: %v", err)
	}
	// if err := checkOutputDir(parsedFlags); err != nil {
	// 	log.Fatalf("[ERR]: %v", err)
	// }
	app, err := newApp(conf)
	if err != nil {
		log.Fatalf("[ERR]: %v", err)
	}
	fmt.Println(*app)
}

func getConfiguration(fl *parsedFlags) (*Config, error) {
	if fl == nil {
		return nil, fmt.Errorf("internal error: passed parsed flags structure is nil")
	}
	config, err := ParseJSONConfig(fl.configFile)
	if err != nil {
		if pErr, ok := err.(*os.PathError); ok {
			if pErr.Op == "open" && pErr.Path == fl.configFile && pErr.Err.Error() == "no such file or directory" {
				return nil, fmt.Errorf("configuration file %s could not be opened, please make sure that the file exists", fl.configFile)
			}
		}
		return nil, err
	}
	config.OutputDir = fl.outputDir
	config.NewGame = fl.newGame
	config.CredsFile = fl.credsFile
	return config, nil
}

type parsedFlags struct {
	configFile string
	outputDir  string
	newGame    bool
	credsFile  string
}

func parseFlags() (*parsedFlags, error) {
	configFile := flag.String("config", "config.json", "configuration file path")
	outputDir := flag.String("out", "", "output dir")
	newGame := flag.Bool("newGame", false, "indicates a new game creation`")
	credentials := flag.String("creds", "", "file that contains credentails for Google sheets API")
	flag.Parse()
	if len(*outputDir) == 0 {
		return nil, fmt.Errorf("flag --o must be set")
	}
	f := &parsedFlags{
		configFile: *configFile,
		outputDir:  *outputDir,
		newGame:    *newGame,
		credsFile:  *credentials,
	}
	return f, nil
}
