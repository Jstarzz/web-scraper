FROM mcr.microsoft.com/playwright:v1.55.0-noble
WORKDIR /app
COPY package.json tsconfig.json ./
RUN npm install --no-audit --no-fund
COPY src ./src
RUN npm run build
ENV BROWSER_PORT=3000
CMD ["node","dist/index.js"]
