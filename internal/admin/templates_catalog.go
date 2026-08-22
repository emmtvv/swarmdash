package admin

// StackTemplate is a built-in, ready-to-deploy compose file offered from
// the template gallery (Stacks -> Templates). Deliberately static/built-in
// only for now, no user-defined templates - see the plan this shipped
// under for why. Each Compose body is meant to be edited (at minimum, the
// placeholder passwords) in the deploy preview step before submitting,
// exactly like pasting compose YAML by hand already works.
type StackTemplate struct {
	ID          string
	Name        string
	Description string
	Compose     string
}

var builtinTemplates = []StackTemplate{
	{
		ID:          "postgres",
		Name:        "PostgreSQL",
		Description: "Single-node Postgres with a named volume for data.",
		Compose: `services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: changeme
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    deploy:
      replicas: 1

volumes:
  pgdata:
`,
	},
	{
		ID:          "mysql",
		Name:        "MySQL",
		Description: "Single-node MySQL with a named volume for data.",
		Compose: `services:
  mysql:
    image: mysql:8.4
    environment:
      MYSQL_ROOT_PASSWORD: changeme
    ports:
      - "3306:3306"
    volumes:
      - mysqldata:/var/lib/mysql
    deploy:
      replicas: 1

volumes:
  mysqldata:
`,
	},
	{
		ID:          "redis",
		Name:        "Redis",
		Description: "Single-node Redis with append-only persistence.",
		Compose: `services:
  redis:
    image: redis:7-alpine
    command: redis-server --appendonly yes
    ports:
      - "6379:6379"
    volumes:
      - redisdata:/data
    deploy:
      replicas: 1

volumes:
  redisdata:
`,
	},
	{
		ID:          "mongodb",
		Name:        "MongoDB",
		Description: "Single-node MongoDB with a named volume for data.",
		Compose: `services:
  mongo:
    image: mongo:7
    environment:
      MONGO_INITDB_ROOT_USERNAME: root
      MONGO_INITDB_ROOT_PASSWORD: changeme
    ports:
      - "27017:27017"
    volumes:
      - mongodata:/data/db
    deploy:
      replicas: 1

volumes:
  mongodata:
`,
	},
	{
		ID:          "nginx",
		Name:        "Nginx",
		Description: "Plain nginx serving its default page - a starting point for a static site.",
		Compose: `services:
  nginx:
    image: nginx:alpine
    ports:
      - "8080:80"
    deploy:
      replicas: 1
`,
	},
	{
		ID:          "adminer",
		Name:        "Adminer",
		Description: "Web-based DB admin UI - point it at any Postgres/MySQL service on the same network.",
		Compose: `services:
  adminer:
    image: adminer:latest
    ports:
      - "8081:8080"
    deploy:
      replicas: 1
`,
	},
}

func findTemplate(id string) (StackTemplate, bool) {
	for _, t := range builtinTemplates {
		if t.ID == id {
			return t, true
		}
	}
	return StackTemplate{}, false
}
