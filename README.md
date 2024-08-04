# AGROWS

**A**lmost **G**ood **R**PC **O**ver **W**eb**S**ockets

## Overview

AGROWS is a framework that allows you to easily execute server-side functions from a WebAssembly (WASM) client using WebSockets. It enables you to define your RPC functions in Golang without worrying about the complexities of client-server communication.

## Installation and Setup

### Prerequisites

- Go 1.22.1 or higher

### Installation

1. Clone the repository:
    ```sh
    git clone https://github.com/codeupdateandmodificationsystem/agrows.git
    cd agrows
    ```

2. Install dependencies:
    ```sh
    go mod download
    ```

## Usage

### Defining RPC Functions

Define your RPC functions in a Go file. Here is an example:
```go
package functions

import "context"

// SayHello sends a greeting
func SayHello(name string) string {
    return "Hello, " + name
}

// CrazyMath performs some math operations
type CalcInput struct {
    A int
    B int
}

func CrazyMath(inp CalcInput) string {
    return fmt.Sprintf("Result: %d", inp.A+inp.B)
}
```

### Generating Client and Server Code

1. Generate the client and server code using the `agrows` CLI:
    
```sh
agrows --input internal/functions/functions.go client agrows --input internal/functions/functions.go server
```
    
2. The generated code will be saved as `agrows_client_functions.go` and `agrows_server_functions.go`.
    

### Running the Server

To start the server, run:

```sh
go run cmd/main.go
```

## Configuration

AGROWS provides the following CLI options:

- `--input`: Specifies the input file containing the RPC functions (required).
- `--output`: Specifies the output file for the generated code (default: `agrows_<server|client>_<input_file>`).
- `--dbg`: Enables debug logging.
- `--compress`: Enables compression in the protocol.

## Example

The usage example repository demonstrates a full application using AGROWS, Templ, TypeScript, and HTMX. It includes development features like auto-reloading. To explore the example:

1. Clone the usage example repository:

```sh
git clone https://github.com/codeupdateandmodificationsystem/agrows-usage-example.git cd agrows-usage-example
```
    
2. Run the example:
    
```sh
just watch
```
    

## Contributing

Contributions are welcome! Please fork the repository and submit a pull request.

## License

AGROWS is released under the GPL license. See `LICENSE` for details.
