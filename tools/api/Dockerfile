FROM node:22.17.1-alpine3.22@sha256:5539840ce9d013fa13e3b9814c9353024be7ac75aca5db6d039504a56c04ea59
WORKDIR /tools
RUN apk add --no-cache postgresql-client
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund
ENV PATH="/tools/node_modules/.bin:${PATH}"
WORKDIR /work/api/bruno
