# go Connect Template CLI

template: github.com/sunmery/connect-example-fast.git

- golang
- sqlc
- otel
- connectrpc/connect
- buf
- protobuf
- consul

# Start

- new project
```shell
co new <project>
```
 
- new microservice project
```shell
co new <path>/<service> --nomod
```

example:
```shell
co new ercommerce
cd ercommerce

co new application/user
```

- added proto CURD file
```shell
kratos proto add <proto file>
```

example:
```shell
co proto add api/helloworld/demo.proto
```

- generate server api
```shell
co proto server <proto path> -t <output path>
```

example:
```shell
co proto server api/user/v1/user.proto -t internal/service/
```
