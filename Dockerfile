from golang:1.22

LABEL BUILD="docker build -t insomniacslk/lol -f Dockerfile ."
LABEL RUN="docker run --rm -it insomniacslk/lol"

WORKDIR /lol

ADD . .

RUN go build

ENTRYPOINT ["./lol", "-c", "config.json"]
