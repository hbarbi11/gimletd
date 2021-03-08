// Copyright 2019 Laszlo Fogas
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ddl

const createTableUsers = "create-table-users"
const createTableEvents = "create-table-events"

type migration struct {
	name string
	stmt string
}

var migrations = map[string][]migration{
	"sqlite3": {
		{
			name: createTableUsers,
			stmt: `
CREATE TABLE IF NOT EXISTS users (
id           INTEGER PRIMARY KEY AUTOINCREMENT,
login         TEXT,
secret        TEXT,
admin         BOOLEAN,
UNIQUE(login)
);
`,
		},
		{
			name: createTableEvents,
			stmt: `
CREATE TABLE IF NOT EXISTS events (
id            TEXT,
created       INTEGER,
type          TEXT,
blob          TEXT,
status        TEXT DEFAULT 'new',
status_desc   TEXT DEFAULT '',
repository    TEXT,
branch        TEXT,
event         TEXT,
source_branch TEXT,
target_branch TEXT,
tag           TEXT,
sha           TEXT,
artifact_id   TEXT,
UNIQUE(id)
);
`,
		},
	},
	"postgres": {},
	"mysql":    {},
}
