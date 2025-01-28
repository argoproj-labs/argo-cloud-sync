FROM debian:latest

ARG BINARY

RUN apt-get -yq update && \
    apt-get -yq install ssh openssl ca-certificates postgresql-client && \
    rm -rf /var/lib/apt/lists/*

COPY $BINARY cello.yaml ./

EXPOSE 8443

CMD [ "./service" ]
