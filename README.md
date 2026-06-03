# go Connect Template CLI

template: github.com/sunmery/connect-example-fast.git

- golang
- sqlc
- otel
- connectrpc/connect
- buf
- protobuf
- consul

# Features

- **Multiple Database Support**: MySQL, PostgreSQL, SQLite
- **Cache Support**: Redis
- **Search Engine Support**: Elasticsearch
- **IAM Support**: Casdoor
- **Build Tools**: Makefile, Taskfile
- **Interactive TUI**: Choose options via interactive prompts or command line flags

# Start

## Installation

```shell
go install
```

## Create New Project

### Interactive Mode (Recommended)

Simply run without any flags to use the interactive TUI:

```shell
co new <service-name>
```

You will be prompted to select:
1. **Database**: mysql, postgres, sqlite, or none
2. **Cache**: redis or none
3. **Search Engine**: es (elasticsearch) or none
4. **IAM**: casdoor or none
5. **Build Tool**: Makefile or Taskfile

### Command Line Mode

Use command line flags for quick setup:

```shell
# Complete command line options
co new <service-name> \
  --database postgres \
  --cache redis \
  --search es \
  --iam none \
  --build makefile

# Mix and match with interactive prompts
co new <service-name> --database mysql
```

## Command Line Options

| Option | Values | Description |
|--------|--------|-------------|
| `--database` | `mysql`, `postgres`, `sqlite`, `none` | Database type |
| `--cache` | `redis`, `none` | Cache system |
| `--search` | `es`, `none` | Search engine (Elasticsearch) |
| `--iam` | `casdoor`, `none` | Identity and Access Management |
| `--build` | `makefile`, `taskfile` | Build tool |
| `--nomod` | - | Create service without go.mod (for monorepo) |

## Examples

### Basic Service with PostgreSQL and Redis

```shell
co new user-service --database postgres --cache redis
```

### Monorepo Microservice with All Features

```shell
co new cart-service --nomod --database postgres --cache redis --search es --iam casdoor --build makefile
```

### Simple Service with MySQL Only

```shell
co new product-service --database mysql --cache none --search none --iam none --build taskfile
```

### Interactive Mode for All Options

```shell
co new order-service
```

## Advanced Usage

### Monorepo Setup

Create microservices without individual go.mod files:

```shell
co new path/to/service --nomod
```

Example for e-commerce monorepo:

```shell
co new ecommerce
cd ecommerce

co new application/user --nomod
co new application/cart --nomod --database postgres --cache redis
co new application/order --nomod --database mysql --search es
```

### Proto Management

- **Add proto file**:
```shell
co proto add api/helloworld/demo.proto
```

- **Generate server API**:
```shell
co proto server api/user/v1/user.proto -t internal/service/
```

## Project Structure

After creating a service, you'll get:

```
<service-name>/
├── api/              # Generated from proto files
├── cmd/              # Application entry points
├── configs/          # Configuration files
├── internal/
│   ├── biz/          # Business logic layer
│   ├── data/         # Data access layer
│   ├── server/       # Server setup
│   └── service/      # Service implementations
├── pkg/              # Internal packages
├── third_party/      # Third-party dependencies
├── Makefile          # Build tasks
└── go.mod            # Go module file
```

## Configuration

Edit `configs/dev.yml` to customize:

```yaml
server:
  addr: "0.0.0.0:30000"

data:
  database:
    postgres:  # or mysql, sqlite
      host: "localhost"
      port: 5432
  cache:
    redis:
      host: "localhost"
      port: 6379
  search:
    elasticsearch:
      addresses:
        - "http://localhost:9200"
  auth:
    casdoor:
      endpoint: "http://localhost:8000"
```

## Development

### Run Locally

```shell
# Using Makefile
make run

# Using Taskfile
task run
```

### Build

```shell
# Using Makefile
make build

# Using Taskfile
task build
```

### Test

```shell
# Using Makefile
make test

# Using Taskfile
task test
```

