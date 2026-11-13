FROM node:22-alpine
WORKDIR /tools
RUN apk add --no-cache postgresql-client
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund
ENV PATH="/tools/node_modules/.bin:${PATH}"
WORKDIR /work/api/bruno
