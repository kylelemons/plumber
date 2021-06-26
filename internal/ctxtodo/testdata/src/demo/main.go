// Copyright 2021 Kyle Lemons
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

var (
	url     = flag.String("url", "", "URL to fetch")
	timeout = flag.Duration("timeout", 30*time.Second, "Timeout for fetch")
)

func main() {
	flag.Parse()

	if err := fetch(http.DefaultClient, *url, *timeout); err != nil {
		log.Fatalf("Error: %s", err)
	}
}

func fetch(client *http.Client, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.TODO(), timeout) // want "Plumb context"
	defer cancel()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("requesting URL: %w", err)
	}
	defer resp.Body.Close()

	for header, values := range resp.Header {
		log.Printf("Header[%q] = %q", header, values)
	}

	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		return fmt.Errorf("copying response: %w", err)
	}

	return nil
}
