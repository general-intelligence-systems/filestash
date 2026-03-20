FROM golang:1.24-trixie AS builder
WORKDIR /home/filestash/
COPY . .
RUN apt-get update > /dev/null && \
    apt-get install -y curl make > /dev/null 2>&1 && \
    apt-get install -y libjpeg-dev libtiff-dev libpng-dev libwebp-dev libraw-dev libheif-dev libgif-dev libvips-dev > /dev/null 2>&1 && \
    make init && \
    make build && \
    mkdir -p ./dist/data/state/config/ && \
    cp config/config.json ./dist/data/state/config/config.json

FROM debian:stable-slim
WORKDIR /app/
COPY --from=builder /home/filestash/dist/ .
RUN apt-get update > /dev/null && \
    apt-get install -y --no-install-recommends apt-utils && \
    apt-get install -y curl ffmpeg libjpeg-dev libtiff-dev libpng-dev libwebp-dev libraw-dev libheif-dev libgif-dev && \
    useradd filestash && \
    chown -R filestash:filestash /app/ && \
    find /app/data/ -type d -exec chmod 770 {} \; && \
    find /app/data/ -type f -exec chmod 760 {} \; && \
    chmod 730 /app/filestash && \
    rm -rf /var/lib/apt/lists/* && \
    rm -rf /tmp/*
USER filestash
CMD ["/app/filestash"]
EXPOSE 8334
