import { describe, expect, it } from "vitest";
import { parseTabular, splitDelimitedRecords, tabularFormatFor, toCsv } from "@/lib/tabular";

function expectOk(result: ReturnType<typeof parseTabular>) {
  if (!result.ok) throw new Error(`expected ok, got: ${result.reason}`);
  return result.table;
}

describe("tabularFormatFor", () => {
  it("recognises the delimited and line-json extensions", () => {
    expect(tabularFormatFor("train.csv")).toBe("csv");
    expect(tabularFormatFor("TRAIN.CSV")).toBe("csv");
    expect(tabularFormatFor("data/train.tsv")).toBe("tsv");
    expect(tabularFormatFor("data.tab")).toBe("tsv");
    expect(tabularFormatFor("events.jsonl")).toBe("jsonl");
    expect(tabularFormatFor("events.ndjson")).toBe("jsonl");
  });

  it("returns null for everything else", () => {
    expect(tabularFormatFor("README.md")).toBeNull();
    expect(tabularFormatFor("model.safetensors")).toBeNull();
    // .json is a single document, not one record per line.
    expect(tabularFormatFor("config.json")).toBeNull();
  });
});

describe("splitDelimitedRecords", () => {
  it("splits plain rows", () => {
    expect(splitDelimitedRecords("a,b\n1,2\n", ",")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });

  it("keeps delimiters and newlines inside quoted fields", () => {
    expect(splitDelimitedRecords('a,b\n"x,y","line1\nline2"\n', ",")).toEqual([
      ["a", "b"],
      ["x,y", "line1\nline2"],
    ]);
  });

  it("unescapes doubled quotes", () => {
    expect(splitDelimitedRecords('q\n"he said ""hi"""\n', ",")).toEqual([["q"], ['he said "hi"']]);
  });

  it("treats CRLF as a single row break", () => {
    expect(splitDelimitedRecords("a,b\r\n1,2\r\n", ",")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });

  it("keeps a final row that is not newline-terminated", () => {
    expect(splitDelimitedRecords("a,b\n1,2", ",")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });

  it("keeps a trailing empty field", () => {
    expect(splitDelimitedRecords("a,b\n1,", ",")).toEqual([
      ["a", "b"],
      ["1", ""],
    ]);
  });

  it("keeps a bare quote inside an unquoted field", () => {
    expect(splitDelimitedRecords('a\n5" pipe\n', ",")).toEqual([["a"], ['5" pipe']]);
  });

  it("does not lose an unterminated quoted field", () => {
    expect(splitDelimitedRecords('a\n"oops\n', ",")).toEqual([["a"], ["oops\n"]]);
  });

  it("splits on tabs when asked", () => {
    expect(splitDelimitedRecords("a\tb\n1\t2\n", "\t")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });

  it("strips a UTF-8 BOM from the first header cell", () => {
    expect(splitDelimitedRecords("﻿a,b\n1,2\n", ",")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });
});

describe("parseTabular (csv/tsv)", () => {
  it("uses the first row as the header", () => {
    const table = expectOk(parseTabular("name,score\nada,9\ngrace,10\n", "csv"));
    expect(table.columns).toEqual(["name", "score"]);
    expect(table.rows).toEqual([
      { name: "ada", score: "9" },
      { name: "grace", score: "10" },
    ]);
    expect(table.malformed).toBe(0);
    expect(table.truncated).toBe(false);
  });

  it("parses TSV with the tab delimiter", () => {
    const table = expectOk(parseTabular("a\tb\n1\t2\n", "tsv"));
    expect(table.columns).toEqual(["a", "b"]);
    expect(table.rows).toEqual([{ a: "1", b: "2" }]);
  });

  it("names blank headers positionally and de-duplicates repeats", () => {
    const table = expectOk(parseTabular("a,,a\n1,2,3\n", "csv"));
    expect(table.columns).toEqual(["a", "column_2", "a_2"]);
    expect(table.rows).toEqual([{ a: "1", column_2: "2", a_2: "3" }]);
  });

  it("pads a short row with nulls and counts it as malformed", () => {
    const table = expectOk(parseTabular("a,b,c\n1,2,3\n4,5\n", "csv"));
    expect(table.rows[1]).toEqual({ a: "4", b: "5", c: null });
    expect(table.malformed).toBe(1);
  });

  it("skips blank lines rather than emitting empty rows", () => {
    const table = expectOk(parseTabular("a,b\n1,2\n\n3,4\n", "csv"));
    expect(table.rows).toHaveLength(2);
    expect(table.malformed).toBe(0);
  });

  it("fails when most rows do not line up with the header", () => {
    const result = parseTabular("a,b,c\n1\n2\n3\n4\n", "csv");
    expect(result.ok).toBe(false);
  });

  it("fails on an empty file", () => {
    expect(parseTabular("", "csv").ok).toBe(false);
    expect(parseTabular("\n\n", "csv").ok).toBe(false);
  });

  it("reports a header-only file as a table with no rows", () => {
    const table = expectOk(parseTabular("a,b\n", "csv"));
    expect(table.columns).toEqual(["a", "b"]);
    expect(table.rows).toEqual([]);
  });
});

describe("parseTabular (jsonl)", () => {
  it("unions keys in first-seen order", () => {
    const table = expectOk(parseTabular('{"a":1,"b":2}\n{"b":3,"c":4}\n', "jsonl"));
    expect(table.columns).toEqual(["a", "b", "c"]);
    expect(table.rows).toEqual([
      { a: 1, b: 2 },
      { b: 3, c: 4 },
    ]);
  });

  it("keeps nested values intact instead of flattening them", () => {
    const table = expectOk(parseTabular('{"a":{"x":1},"b":[1,2]}\n', "jsonl"));
    expect(table.rows[0]).toEqual({ a: { x: 1 }, b: [1, 2] });
  });

  it("ignores blank lines and trailing CR", () => {
    const table = expectOk(parseTabular('{"a":1}\r\n\r\n{"a":2}\r\n', "jsonl"));
    expect(table.rows).toEqual([{ a: 1 }, { a: 2 }]);
  });

  it("tolerates a single bad line among many", () => {
    const lines = Array.from({ length: 30 }, (_, i) => `{"a":${i}}`);
    lines.push("not json");
    const table = expectOk(parseTabular(`${lines.join("\n")}\n`, "jsonl"));
    expect(table.rows).toHaveLength(30);
    expect(table.malformed).toBe(1);
  });

  it("fails when the file is not line-delimited JSON at all", () => {
    const result = parseTabular("just some prose\nand more prose\n", "jsonl");
    expect(result.ok).toBe(false);
  });

  it("fails on scalars and arrays, which have no columns", () => {
    expect(parseTabular("1\n2\n3\n", "jsonl").ok).toBe(false);
    expect(parseTabular("[1,2]\n[3,4]\n", "jsonl").ok).toBe(false);
  });

  it("fails on an empty file", () => {
    expect(parseTabular("", "jsonl").ok).toBe(false);
  });
});

describe("toCsv", () => {
  it("writes a header row followed by the values", () => {
    expect(toCsv(["a", "b"], [{ a: "1", b: "2" }])).toBe("a,b\n1,2");
  });

  it("quotes fields containing a delimiter, quote or newline", () => {
    const csv = toCsv(["a"], [{ a: "x,y" }, { a: 'he said "hi"' }, { a: "l1\nl2" }]);
    expect(csv).toBe('a\n"x,y"\n"he said ""hi"""\n"l1\nl2"');
  });

  it("writes missing values as empty and structures as JSON", () => {
    expect(toCsv(["a", "b"], [{ a: null, b: { x: 1 } }])).toBe('a,b\n,"{""x"":1}"');
  });

  it("round-trips through the parser", () => {
    const rows = [{ name: "ada, lovelace", note: 'said "hi"' }];
    const parsed = parseTabular(toCsv(["name", "note"], rows), "csv");
    expect(parsed.ok && parsed.table.rows).toEqual(rows);
  });

  describe("formula injection", () => {
    it("neutralises values starting with =, @, tab or CR", () => {
      // Contains quotes and a comma too, so the escaped `'...` value also
      // needs the ordinary CSV quoting rules applied on top.
      expect(toCsv(["a"], [{ a: '=HYPERLINK("http://evil","click")' }])).toBe(
        'a\n"\'=HYPERLINK(""http://evil"",""click"")"',
      );
      expect(toCsv(["a"], [{ a: "@SUM(A1:A2)" }])).toBe("a\n'@SUM(A1:A2)");
      expect(toCsv(["a"], [{ a: "\tcmd" }])).toBe("a\n'\tcmd");
      // The CR itself also triggers the ordinary CSV quoting rule.
      expect(toCsv(["a"], [{ a: "\rcmd" }])).toBe('a\n"\'\rcmd"');
    });

    it("neutralises non-numeric values starting with + or -", () => {
      expect(toCsv(["a"], [{ a: "-cmd|' /C calc'!A0" }])).toBe("a\n'-cmd|' /C calc'!A0");
      expect(toCsv(["a"], [{ a: "+cmd" }])).toBe("a\n'+cmd");
    });

    it("does not mangle a header cell that carries the attack", () => {
      // Run names, tags, groups and metric keys become header cells too.
      expect(toCsv(["=cmd|calc"], [{ "=cmd|calc": "1" }])).toBe("'=cmd|calc\n1");
    });

    it("quotes an escaped value that also needs CSV quoting", () => {
      expect(toCsv(["a"], [{ a: "=A1,B1" }])).toBe('a\n"\'=A1,B1"');
    });

    it("leaves plain signed numbers untouched", () => {
      expect(toCsv(["a"], [{ a: "-1.5" }])).toBe("a\n-1.5");
      expect(toCsv(["a"], [{ a: "+42" }])).toBe("a\n+42");
      expect(toCsv(["a"], [{ a: "-1.5e10" }])).toBe("a\n-1.5e10");
      expect(toCsv(["a"], [{ a: "-.5" }])).toBe("a\n-.5");
    });

    it("still escapes a value that merely starts like a number", () => {
      expect(toCsv(["a"], [{ a: "-1.5.6" }])).toBe("a\n'-1.5.6");
      expect(toCsv(["a"], [{ a: "-1a" }])).toBe("a\n'-1a");
    });

    it("leaves ordinary text and existing quoting rules alone", () => {
      expect(toCsv(["a"], [{ a: "hello" }])).toBe("a\nhello");
      expect(toCsv(["a"], [{ a: "x,y" }])).toBe('a\n"x,y"');
    });
  });
});
