package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"time"

	"strings"
	"testing"

	"github.com/flair-sdk/erpc/util"
	"github.com/h2non/gock"
	"github.com/rs/zerolog"
	"github.com/spf13/afero"
)

func TestMain_RealConfigFile(t *testing.T) {
	fs := afero.NewOsFs()

	f, err := afero.TempFile(fs, "", "erpc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	localHost := "localhost"
	localPort := fmt.Sprint(rand.Intn(1000) + 2000)
	localBaseUrl := fmt.Sprintf("http://localhost:%s", localPort)
	f.WriteString(`
logLevel: DEBUG

server:
  httpHost: "` + localHost + `"
  httpPort: ` + localPort + `
`)

	os.Args = []string{"erpc-test", f.Name()}
	go main()

	time.Sleep(300)

	// check if the server is running
	if _, err := http.Get(localBaseUrl); err != nil {
		t.Fatalf("expected server to be running, got %v", err)
	}
}

func TestMain_MissingConfigFile(t *testing.T) {
	os.Args = []string{"erpc-test", "some-random-non-existent.yaml"}

	originalOsExit := util.OsExit
	var called bool
	defer func() {
		util.OsExit = originalOsExit
	}()
	util.OsExit = func(code int) {
		if code != util.ExitCodeERPCStartFailed {
			t.Errorf("expected code %d, got %d", util.ExitCodeERPCStartFailed, code)
		} else {
			called = true
		}
	}

	go main()

	time.Sleep(300 * time.Millisecond)

	if !called {
		t.Error("expected osExit to be called")
	}
}

func TestMain_InvalidHttpPort(t *testing.T) {
	fs := afero.NewOsFs()

	f, err := afero.TempFile(fs, "", "erpc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`
logLevel: DEBUG

server:
  httpHost: "localhost"
  httpPort: "-1"
`)

	os.Args = []string{"erpc-test", f.Name()}

	originalOsExit := util.OsExit
	var called bool
	defer func() {
		util.OsExit = originalOsExit
	}()
	util.OsExit = func(code int) {
		if code != util.ExitCodeHttpServerFailed {
			t.Errorf("expected code %d, got %d", util.ExitCodeHttpServerFailed, code)
		} else {
			called = true
		}
	}

	go main()

	time.Sleep(300 * time.Millisecond)

	if !called {
		t.Error("expected osExit to be called")
	}
}

func TestInit_HappyPath(t *testing.T) {
	defer gock.Disable()
	defer gock.DisableNetworking()
	defer gock.DisableNetworkingFilters()

	gock.EnableNetworking()

	// Register a networking filter
	gock.NetworkingFilter(func(req *http.Request) bool {
		shouldMakeRealCall := strings.Split(req.URL.Host, ":")[0] == "localhost"
		return shouldMakeRealCall
	})

	//
	// 1) Initialize the eRPC server with a mock configuration
	//
	fs := afero.NewMemMapFs()
	cfg, err := afero.TempFile(fs, "", "erpc.yaml")
	if err != nil {
		t.Fatal(err)
	}

	localHost := "localhost"
	localPort := fmt.Sprint(rand.Intn(1000) + 2000)
	localBaseUrl := fmt.Sprintf("http://localhost:%s", localPort)
	cfg.WriteString(`
logLevel: DEBUG

server:
  httpHost: "` + localHost + `"
  httpPort: ` + localPort + `

projects:
  - id: main
    upstreams:
    - id: good-evm-rpc
      endpoint: http://google.com
      metadata:
        evmChainId: 1
`)
	args := []string{"erpc-test", cfg.Name()}

	shutdown, err := Init(fs, args)
	if shutdown != nil {
		defer shutdown()
	}
	if err != nil {
		t.Fatal(err)
	}

	//
	// 2) Create a new mock EVM JSON-RPC server
	//
	gock.New("http://google.com").
		Post("").
		MatchType("json").
		JSON(
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x1273c18",false]}`),
		).
		Reply(200).
		JSON(json.RawMessage(`{"result":{"hash":"0x64d340d2470d2ed0ec979b72d79af9cd09fc4eb2b89ae98728d5fb07fd89baf9"}}`))

	//
	// 3) Make a request to the eRPC server
	//
	body := bytes.NewBuffer([]byte(`
		{
			"method": "eth_getBlockByNumber",
			"params": [
				"0x1273c18",
				false
			],
			"id": 91799,
			"jsonrpc": "2.0"
		}
	`))
	res, err := http.Post(fmt.Sprintf("%s/main/1", localBaseUrl), "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", res.StatusCode)
	}
	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("error reading response: %s", err)
	}

	//
	// 4) Assert the response
	//
	respObject := make(map[string]interface{})
	err = json.Unmarshal(respBody, &respObject)
	if err != nil {
		t.Fatalf("error unmarshalling: %s response body: %s", err, respBody)
	}

	if respObject["hash"] != "0x64d340d2470d2ed0ec979b72d79af9cd09fc4eb2b89ae98728d5fb07fd89baf9" {
		t.Errorf("unexpected hash, got %s", respObject["hash"])
	}
}

func TestInit_InvalidConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg, err := afero.TempFile(fs, "", "erpc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.WriteString("invalid yaml")

	args := []string{"erpc-test", cfg.Name()}

	shutdown, err := Init(fs, args)
	if shutdown != nil {
		defer shutdown()
	}

	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if !strings.Contains(err.Error(), "failed to load configuration") {
		t.Errorf("unexpected error: %s", err)
	}
}

func TestInit_ConfigFileDoesNotExist(t *testing.T) {
	fs := afero.NewMemMapFs()
	args := []string{"erpc-test", "non-existent-file.yaml"}

	shutdown, err := Init(fs, args)
	if shutdown != nil {
		defer shutdown()
	}

	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("unexpected error: %s", err)
	}
}

func TestInit_InvalidLogLevel(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg, err := afero.TempFile(fs, "", "erpc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.WriteString("logLevel: invalid")

	args := []string{"erpc-test", cfg.Name()}

	shutdown, err := Init(fs, args)
	if err != nil {
		t.Fatal(err)
	}
	if shutdown != nil {
		defer shutdown()
	}

	logLevel := zerolog.GlobalLevel()
	if logLevel != zerolog.DebugLevel {
		t.Errorf("expected log level to be DEBUG, got %s", logLevel)
	}
}

func TestInit_BootstrapFailure(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg, err := afero.TempFile(fs, "", "erpc.yaml")
	if err != nil {
		t.Fatal(err)
	}

	localHost := "localhost"
	localPort := fmt.Sprint(rand.Intn(1000) + 2000)
	cfg.WriteString(`
logLevel: DEBUG

server:
  httpHost: "` + localHost + `"
  httpPort: ` + localPort + `

projects:
  - id: main
    upstreams:
    - id: good-evm-rpc
      endpoint: http://google.com
      # NOT providing chain ID will cause the bootstrap to fail
      # metadata:
      #  evmChainId: 1
`)
	args := []string{"erpc-test", cfg.Name()}

	shutdown, err := Init(fs, args)
	if shutdown != nil {
		defer shutdown()
	}

	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if !strings.Contains(err.Error(), "cannot bootstrap") {
		t.Errorf("unexpected error: %s", err)
	}
}