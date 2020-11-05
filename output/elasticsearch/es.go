package elasticsearch

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

var (
	Trace   *log.Logger
	Info    *log.Logger
	Warning *log.Logger
	Error   *log.Logger
	Debug   *log.Logger
	Default *log.Logger
)

type EsConfig struct {
	Host  string `yaml:"host"`
	Index string `yaml:"index"`
}

func Init() {
	file, err := os.OpenFile(path.Join("logs", "runtime.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalln("Failed to open error log file:", err)
	}

	Trace = log.New(io.MultiWriter(file, os.Stdout),
		"[OUTPUT TRACE]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Info = log.New(io.MultiWriter(file, os.Stdout),
		"[OUTPUT INFO]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Warning = log.New(io.MultiWriter(file, os.Stdout),
		"[OUTPUT WARNING]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Error = log.New(io.MultiWriter(file, os.Stderr),
		"[OUTPUT ERROR]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Debug = log.New(os.Stdout,
		"[OUTPUT DEBUG]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Default = log.New(io.MultiWriter(file, os.Stdout), "", 0)
}

func Execute(output EsConfig, outputCh chan interface{}) {
	Init()

	// Create a context object for the API calls
	ctx := context.Background()

	// Declare an Elasticsearch configuration
	cfg := elasticsearch.Config{
		Addresses: []string{
			output.Host,
		},
	}

	// Instantiate a new Elasticsearch client object instance
	client, err := elasticsearch.NewClient(cfg)

	if err != nil {
		Error.Fatalln("Elasticsearch connection error:", err)
	}

	// Have the client instance return a response
	if res, err := client.Info(); err != nil {
		Error.Fatalln("client.Info() ERROR:", err)
	} else {
		Info.Println("client response:", res)
	}

	for {
		kvMap := <-outputCh
		body, err := json.Marshal(kvMap)
		if err != nil {
			Error.Printf("Failed to convert to json: %s\n", err)
			continue
		}
		// ask es to generate doc ID automatically
		docID := ""

		Info.Println(string(body))
		// Instantiate a request object
		req := esapi.IndexRequest{
			Index:      output.Index,
			DocumentID: docID,
			Body:       strings.NewReader(string(body)),
			Refresh:    "true",
		}

		// Return an API response object from request
		res, err := req.Do(ctx, client)
		if err != nil {
			Error.Printf("IndexRequest ERROR: %s\n", err)
		}
		defer res.Body.Close()

		if res.IsError() {
			Error.Printf("%s ERROR indexing document ID=%d\n", res.Status(), docID)
		} else {
			// Deserialize the response into a map.
			var resMap map[string]interface{}
			if err := json.NewDecoder(res.Body).Decode(&resMap); err != nil {
				Error.Printf("Error parsing the response body: %s\n", err)
			} else {
				// Print the response status and indexed document version.
				Trace.Printf("IndexRequest() RESPONSE: \nStatus: %s\n Result: %s\n Version:%s\n KvMap:%+v\n ",
					res.Status(), resMap["result"], int(resMap["_version"].(float64)), resMap)
			}
		}
	}
}
