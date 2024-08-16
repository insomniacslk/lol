from golang:1.22

LABEL BUILD="docker build -t insomniacslk/lol -f Dockerfile ."
LABEL RUN="docker run --rm -it insomniacslk/lol"

WORKDIR /app

ADD . .

RUN go build
RUN mv config.json.example config.json

ENTRYPOINT ["/app/lol", "-c", "/app/config.json"]
