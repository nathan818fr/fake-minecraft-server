FROM docker.io/library/node:20.6.1-alpine3.18

ENV NODE_ENV=production

COPY . /app/

WORKDIR /app

RUN npm ci --no-audit --no-fund --logs-dir=/dev/null && { npm cache clean --force --logs-dir=/dev/null || true; }

USER node
CMD [ "node", "/app/src/index.js" ]
