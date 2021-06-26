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
	"net/http"
)

func a() {
	_ = context.TODO() // want "Plumb context"
}

func b(ctx context.Context) {
	a()
}

func c() {
	ctx := context.Background()
	a()
	_ = ctx
}

func d(ctx context.Context) {
	_ = context.TODO() // want "Plumb context"
}

func e(r *http.Request) {
	a()

	ctx := context.Background()
	_ = ctx
}

func f(*http.Request) { // want "Name this param if you want plumber to use it"
	a()
}

func g() {
	func(ctx context.Context) {
		a()
	}(nil)
	func(*http.Request) { // want "Name this param if you want plumber to use it"
		a()
	}(nil)
}

func h() {
	a()

	func() {
		var r *http.Request
		a()
		_ = r
	}()

	var r *http.Request
	a()
	_ = r
}

func i(r *http.Request) {
	foo := context.TODO() // want "Plumb context"
	_ = foo
}

func j(r *http.Request) {
	ctx := context.TODO() // want "Plumb context"
	_ = ctx
}

func main() {
	a()
	a()
}

func init() {
	a()
}
