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

package basic

import (
	"context"
	"log"
	"net"
)

func a() {
	ctx := context.TODO() // want "Plumb context"
	_ = ctx
}

func b(addr string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return dialer.DialContext(context.TODO(), "tcp", addr) // want "Plumb context"
}

func c() {
	conn, err := b("localhost:12345")
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	_ = context.TODO() // want "Plumb context"
}

func cycle1() {
	log.Println(context.TODO()) // want "Plumb context"
	cycle2()
}

func cycle2() {
	cycle1()
}

type t struct{}

func (t) m() { _ = context.TODO() } // want "Plumb context"
