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
	"fmt"
	"log"
	"net/http"

	"multi/producer"
)

func main() {
	client, err := producer.Dial("localhost") // want "Continue plumbing context"
	if err != nil {
		log.Fatal(err)
	}
	client.Noop() // want "Continue plumbing context"
}

func repro() {
	var db interface{ Close(context.Context) error }
	_ = func(r *http.Request) {
		defer func() {
			if err := db.Close(context.TODO()); err != nil { // want "Plumb context"
				_ = err
			}
		}()
	}
}

func repro2() {
	func(r *http.Request) error {
		if _, err := producer.Dial("addr"); err != nil { // want "Continue plumbing context"
			return fmt.Errorf("etl: ingest: %w", err)
		}

		return nil
	}(nil)
}
