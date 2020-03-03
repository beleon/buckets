# <img src="https://github.com/sellleon/buckets/raw/master/logo.png" width="25px"/> Buckets
[![Go Report Card](https://goreportcard.com/badge/github.com/sellleon/buckets)](https://goreportcard.com/report/github.com/sellleon/buckets)
[![MIT license](https://img.shields.io/badge/license-MIT-brightgreen.svg)](https://github.com/sellleon/buckets/blob/master/LICENSE)

A tiny and fast in-memory pastbin with curl support. Buckets is a ~350 lines single file go project with no dependencies which
serves a single file HTML page with no dependencies using only vanilla javascript. The services can be hosted via
docker and accessed using simple curl commands. It has no means of authentication and stores all data in memory; no 
databases.

## Usage

Either open your browser at `http://localhost:8080` or use the API directly:

### Storing Buckets
To store a bucket send a POST request to `http://localhost:8080` with the message or file as the request body. Storing
a message with curl would look like this:

```bash
curl -X POST --data "Hello, World" http://localhost:8080
```

A file upload would look like this:

```bash
curl -X POST --data-binary @path/to/file http://localhost:8080
```

The response is the URL where your data is stored at. E.g. `http://localhost:8080/shbo`

You can also store a bucket at a location of your choice by sending a POST request to that location:

```bash
curl -X POST --data-binary @path/to/file http://localhost:8080/my/custom/bucket/location
```

Now your bucket is stored at `http://localhost:8080/my/custom/bucket/location`.

WARNING: This will replace any data that was stored at this location before.

### Retrieving Buckets
Simply send a GET request to the URL your bucket is stored at. E.g. through opening the address in your
browser or using curl:

```bash
curl http://localhost:8080/shbo
```

If your bucket stores binary data you can save the data into a file using:

```bash
curl -o path/to/savefile http://localhost:8080/shbo
```

### Deleting Buckets
To delete a bucket send a DELETE request to the URL your bucket is stored at. E.g. using curl:

```bash
curl -X DELETE http://localhost:8080/shbo
```

## Setup

### Build Project

Working go installation and git needed.

clone project:

```bash
git clone https://github.com/sellleon/buckets
```

change into project dir:

```bash
cd buckets
```

build project:

```bash
go build
```

run:

```bash
./buckets
```

### Docker

Working docker installation and git needed.

clone project:

```bash
git clone https://github.com/sellleon/buckets
```

change into project dir:

```bash
cd buckets
```

build docker image:

```bash
docker image build -t buckets:0.1 .
```

run docker:

```bash
usr/bin/docker run --rm --name=buckets -p 8080:8080 buckets:0.1
```

or run docker with some environment variables (time to live 2 days, store a maximum of 20 buckets):

```bash
usr/bin/docker run --rm --name=buckets -p 8080:8080 --env BUCKETS_TTL=172800 --env BUCKETS_MAX_BUCKETS=20 buckets:0.1
```

## Configuration

A couple of properties can be set via environment variables:

| Env Name                 |  Description                                                                                                                                                                                                                                                            |
|--------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| BUCKETS_BASE_URL         | URL location of your service. Default `http://localhost:8080`. Used for generating links of buckets and for the HTML page.                                                                                                                                              |
| BUCKETS_TTL              | A bucket's time to live. Determines how long a bucket will be stored. Default is 2 days. Use 0 for indefinite storage of buckets.                                                                                                                                       |
| BUCKETS_MAX_STORAGE_SIZE | Maximum size that the sum of all buckets can have in MB (i.e. 1000000 bytes) as a float64 value. Oldest buckets will be delete until enough free space is available if new bucket would cause the storage size to be larger than the MAX_STORAGE_SIZE. Default is 1000. |
| BUCKETS_MAX_BUCKETS      | Maximum amount of buckets that can be stored at once. Oldest bucket will be deleted when inserting new bucket when maximum number of buckets is reached. Default is 1000.                                                                                               |
| BUCKETS_CHARSET          | The character set that will be used to generate the location of the buckets. Default is a-z.                                                                                                                                                                            |
| BUCKETS_SLUG_SIZE        | Length of the generated bucket location. Default is 4.                                                                                                                                                                                                                  |
| BUCKETS_SEED             | Mostly for debugging. Seed is used for generating the location of new buckets. Default is current time.                                                                                                                                                                 |

If you want to use TLS/SSL or want to constrain the size of a bucket I'd suggest using nginx with proxy_pass.
