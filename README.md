# plumber

The `plumber` command is a utility to do a lot of the manual labor of plumbing `context.Context`
through many layers of calls, especially between packages.

## Installation

To install plumber, run:

    $ go install github.com/kylelemons/plumber@latest

## Usage

> **Warning**: Plumber is likely to change _a lot_ of code.
> Only run it on a clean working directory of version-controlled software.

Usage of this tool is pretty straightforward:

1. Add `context.TODO()` wherever you need a `context.Context`.
1. Run `plumber --fix` with the packages (usually `./...`) you want to consider in-scope.
   * **Note** that the output will look like it's finding problems; this is intended,
     as most of the problems that are identified also provide suggested fixes
     that are automatically applied by the `--fix` parameter.
   * It bears repeating: **make sure your working directory is clean before running with `--fix`**.
     Don't say I didn't warn you.
1. Review the changes and adjust where necessary.

### Example

As a simple example, this snippet:

    func main() {
        flag.Parse()

        if err := fetch(http.DefaultClient, *url, *timeout); err != nil {
            log.Fatalf("Error: %s", err)
        }
    }

    func fetch(client *http.Client, url string, timeout time.Duration) error {
        ctx, cancel := context.WithTimeout(context.TODO(), timeout)
        defer cancel()

        req, err := http.NewRequest(http.MethodGet, url, nil)
        if err != nil {
            return fmt.Errorf("creating request: %w", err)
        }

        // ... see internal/ctxtodo/testdata/src/demo/main.go for more

        return nil
    }

After running

    $ plumber --fix ./...

will have the following changes:

1. `main` will have `ctx := context.Background()`
1. `fetch` will have a new `ctx context.Context` parameter
1. `context.TODO()` will be replaced by `ctx`

## Details

The `ctxtodo` analyzer that powers `plumber` has a few core jobs:

1. It builds a call graph within each package
1. It marks exported functions that are being changed
1. It locates calls that need to be updated
   * Either explicitly marked with `context.TODO()`, or
   * Implicitly for calls into packages it's already analyzed
1. It walks the call graph, locating and creating sources of contexts. It tries the following:
   * Formal parameters
   * Local variables
   * A new `ctx := context.Background()` (in "entrypoint" functions like `main` or `TestFoo`)
   * A new `ctx context.Context` parameter
    
Contexts can be sourced in two ways:
* A variable that is explicitly a `context.Context`
* A value with a `Context() context.Context` method.  Examples:
  * `*http.Request`
  * `*cobra.Command`
    
## Known deficiencies

Currently the `ctxtodo` analyzer can't deal with certain things:
* It cannot add names to unnamed parameters, even if it wants to use them as a context source
  * In this case it will use a fake variable like `unnamedParam0`
* It can't know if the context it could get from a `Context()` method is meaningful
* It doesn't know when or whether to add parameters to closures
  * It will use a closure parameter if it's there, but it will only add parameters to top-level functions.
* It expects to operate on a large corpus at once
  * It will happily update exported methods, but any callers that it can't find
    will be on their own.
* It doesn't follow variables, interfaces, etc.