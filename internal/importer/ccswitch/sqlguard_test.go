package ccswitch

import "testing"

func TestGuardIgnoresSQLLookingDataAndEscapedQuotes(t *testing.T) {
	for _, script := range []string{
		`INSERT INTO providers VALUES ('ATTACH ''db''; VACUUM INTO ''x'';');`,
		`INSERT INTO providers VALUES ('-- comment; /* comment */');`,
		`CREATE TABLE "with;semicolon" ("a""b" TEXT); INSERT INTO "with;semicolon" VALUES ('semi;colon');`,
		"CREATE TABLE `a``b` ([x] TEXT);",
		"-- harmless ATTACH\n/* VACUUM */ PRAGMA foreign_keys=OFF;",
		`PRAGMA 'foreign_keys'(1); PRAGMA "user_version" = 18;`,
	} {
		if err := guardSQL(script); err != nil {
			t.Fatal("safe SQL tokenization failed")
		}
	}
}

func FuzzGuardSQL(f *testing.F) {
	f.Add(exportHeader + "\n" + minimalSQL)
	f.Add("INSERT INTO t VALUES ('a''b;/*c*/');")
	f.Add("CREATE TRIGGER a BEFORE INSERT ON t BEGIN SELECT writefile('x','y'); END;")
	f.Fuzz(func(t *testing.T, script string) {
		if len(script) > 1<<20 {
			t.Skip()
		}
		_ = guardSQL(script)
	})
}
