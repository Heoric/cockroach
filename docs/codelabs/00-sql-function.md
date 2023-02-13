# Codelab: Adding a SQL Function

## Background

This codelab will walk you through adding a new SQL function and its associated
tests.
此 Codelab 将引导您完成添加新 SQL 函数及其相关测试的过程。

## Getting Started

Before we get started, you need to download the CockroachDB source code and
ensure you have all of the prerequisites needed for development. See
[CONTRIBUTING.md] doc for details.
在我们开始之前，您需要下载 CockroachDB 源代码并确保您具备开发所需的所有先决条件。

## Adding a SQL Function

Currently, CockroachDB only supports [built-in SQL functions][built-ins]. We’re
going to walk through the process of adding a new built-in function and an
associated test. Along the way you’ll see a bit of the SQL code layout, parts of
the type system and part of the logic test infrastructure.
我们将逐步完成添加新内置函数和相关测试的过程。 在此过程中，您将看到一些 SQL 代码布局、部分类型系统和部分逻辑测试基础结构。

### Built-ins

The SQL code lies within the `pkg/sql` directory. The built-in
functions reside in `pkg/sql/sem/builtins/builtins.go`. A function is
described by a `Overload` structure, in `pkg/sql/sem/tree/overload.go`:

```go
type Overload struct {
  Types      TypeList
  ReturnType ReturnTyper
  ...
  Fn         func(*EvalContext, Datums) (Datum, error)
}
```

`Overload` contains a number of fields, reflecting the
diversity of built-in functions. Three important fields for us to pay
attention to are the argument types (`Types`), the return type
(`ReturnType`) and the implementation function pointer (`Fn`).
`Overload`包含多个字段，体现了内置函数的多样性。 我们需要注意的三个重要字段是参数类型 (`Types`)、返回类型 (`ReturnType`) 和实现函数指针 (`Fn`)。

Multiple function overloads are then grouped into a single "built-in
definition" (`builtinDefinition` in `builtins/builtins.go`), and
during CockroachDB initialization transformed into a
`FunctionDefinition` (in `builtins/all_builtins.go`).
然后将多个函数重载分组到一个“内置定义”（builtins/builtins.go 中的 builtinDefinition），并在 CockroachDB 初始化期间转换为 FunctionDefinition（builtins/all_builtins.go）。

For example, `abs` has an overload for each numeric type (`float`,
`decimal`, and `int`). The type system takes care of selecting the
correct version of a function given the name and the argument
types.
例如，`abs` 对每种数字类型（`float`、`decimal` 和 `int`）都有一个重载。 类型系统负责根据名称和参数类型选择函数的正确版本。

The SQL execution engine finds the `builtinDefinition` structure
given the name of a function using the `builtins` map:
SQL 执行引擎使用 builtins 映射找到给定函数名称的 builtinDefinition 结构：

```go
var builtins = map[string]builtinDefinition{...}
```

Notice that this is a map from `string` to `builtinDefinition`, which
contains a slice of `Overload`s via the member field
`Overloads`. The `Overloads` slice is used to distinguish the
"overloads" for a given function.
请注意，这是一个从“string”到“builtinDefinition”的映射，它通过成员字段“Overloads”包含一片“Overload”。 `Overloads` 切片用于区分给定函数的“重载”。

### What’s Your Name

We’re going to add a new SQL function: `whois()`. This function will take a
variable number of usernames and return the corresponding real names. For
example, `whois('pmattis')` will return `'Peter Mattis'`. For simplicity, the
mapping of usernames to real names will be hardcoded. Let’s get started.
我们将添加一个新的 SQL 函数：`whois()`。 此函数将采用可变数量的用户名并返回相应的真实姓名。 例如，`whois('pmattis')` 将返回 `'Peter Mattis'`。 为简单起见，用户名到真实姓名的映射将被硬编码。 让我们开始吧。

The `builtins` map is divided up into sections by function category, but this
organization is purely for readability. We can add our function anywhere, so
let’s add it right at the top of the definition for simplicity:
`builtins` 映射按功能类别划分为多个部分，但这种组织纯粹是为了便于阅读。 我们可以在任何地方添加我们的函数，所以为了简单起见，让我们把它添加到定义的顶部：

```go
var builtins = map[string]builtinDefinition{
  "whois": makeBuiltin(defProps(),
    tree.Overload{
      Types:      tree.VariadicType{VarType: types.String},
      ReturnType: tree.FixedReturnType(types.String),
      Fn: func(ctx *tree.EvalContext, args tree.Datums) (tree.Datum, error) {
        return tree.DNull, fmt.Errorf("nothing to see here")
      },
    },
  ),
  ...
```

This is the skeleton of our built-in. The `Types` field indicates our function
takes a variable number of string arguments. The `ReturnType` field indicates
our function returns a string. The implementation of our function is currently
unfinished, so we’ll return an error for now.
这是我们内置的骨架。 `Types` 字段表示我们的函数采用可变数量的字符串参数。 `ReturnType` 字段表示我们的函数返回一个字符串。 我们函数的实现目前尚未完成，所以我们暂时返回一个错误。

Go ahead and add the above code to `pkg/sql/sem/builtins/builtins.go`. If you’ve
followed the instructions in [CONTRIBUTING.md], you should be able to build
CockroachDB from source:
继续将上面的代码添加到 pkg/sql/sem/builtins/builtins.go 中。 如果您按照 [CONTRIBUTING.md] 中的说明进行操作，您应该能够从源代码构建 CockroachDB：

```text
~/go/src/github.com/cockroachdb/cockroach$ make build
...
github.com/cockroachdb/cockroach
```

Now, let’s run a single-node Cockroach instance:

```text
$ rm -fr cockroach-data/ && ./cockroach start --insecure
...
status:     initialized new cluster
...
```

In another terminal window, use the `cockroach sql` shell to execute our
built-in:

```text
$ ./cockroach sql --insecure -e "select whois()"
Error: pq: whois(): nothing to see here
Failed running "sql"
```

Yay! We successfully added our built-in function and it failed to execute. Note
that the error message above is due to our implementation. If we try to execute
a non-existent function we’d get a different error:
耶！ 我们成功添加了内置函数，但它无法执行。 请注意，上面的错误消息是由于我们的实现造成的。 如果我们尝试执行一个不存在的函数，我们会得到一个不同的错误：

```go
$ ./cockroach sql --insecure -e 'select nonexistent()'
Error: pq: unknown function: nonexistent()
Failed running "sql"
```

Our built-in is going to map usernames to real names. For that we’ll need a map:
我们的内置将把用户名映射到真实姓名。 为此，我们需要一张地图：

```go
users := map[string]string{
  "bdarnell": "Ben Darnell",
  "pmattis":  "Peter Mattis",
  "skimball": "Spencer Kimball",
}
```

We’ll need to loop over the arguments to the function and look up the
corresponding real names:
我们需要遍历函数的参数并查找相应的真实姓名：

```go
var buf bytes.Buffer
for i, arg := range args {
  // Because we specified the type of this function as
  // Variadic{Typ: types.String}, the type system will ensure that all
  // arguments are strings, so we can perform a simple type assertion on
  // each argument to access the string within.
  username := string(*arg.(*tree.DString))
  name, ok := users[strings.ToLower(username)]
  if !ok {
    return tree.DNull, fmt.Errorf("unknown username: %s", arg)
  }
  if i > 0 {
    buf.WriteString(", ")
  }
  buf.WriteString(name)
}
```

Lastly, we need to return the result:

```go
return tree.NewDString(buf.String()), nil
```

Much of the above looks like standard Go, but what is a ``DString``? The SQL
execution engine has its own typing system. Each type in the system adheres to
the ``Datum`` interface which defines the methods that a type needs to
implement. ``DString`` is the implementation of ``Datum`` for the SQL ``string``
type.
上面的大部分看起来像标准的 Go，但什么是“DString”？ SQL 执行引擎有自己的类型系统。 系统中的每个类型都遵循“Datum”接口，该接口定义了一个类型需要实现的方法。 ``DString`` 是 SQL ``string`` 类型的 ``Datum`` 的实现。

```go
type DString string
```

Note that `*DString` implements the `Datum` interface, not `DString`. This is why
we type assert the arguments using `arg.(*DString)`.

Put it all together (rebuild, restart your server) and we should have a working
function:

```text
$ ./cockroach sql --insecure -e "select whois('pmattis')"
+------------------+
| whois('pmattis') |
+------------------+
| Peter Mattis     |
+------------------+
(1 row)

$ ./cockroach sql --insecure -e "select whois('pmattis', 'bdarnell')"
+------------------------------+
| whois('pmattis', 'bdarnell') |
+------------------------------+
| Peter Mattis, Ben Darnell    |
+------------------------------+
(1 row)

$ ./cockroach sql --insecure -e "select whois('non-existent')"
Error: pq: whois(): unknown username: 'non-existent'
Failed running "sql"
```

So far so good. One oddity of our function is that it returns the empty string
if there are no arguments. Let’s make it return all of the users in that case.
Before the loop over the arguments, we check to see if no arguments were
specified and expand that to a list of all of the usernames:

```go
if len(args) == 0 {
  args = make(tree.Datums, 0, len(users))
  for user := range users {
    args = append(args, tree.NewDString(user))
  }
}
var buf bytes.Buffer
for i, arg := range args {
  ...
}
```

Rebuild, restart and test:

```text
$ ./cockroach sql --insecure -e "select whois()"
+--------------------------------------------+
|                  whois()                   |
+--------------------------------------------+
| Ben Darnell, Peter Mattis, Spencer Kimball |
+--------------------------------------------+
(1 row)
```

Nice!

## Testing Our New Function

Now, it’s time to codify the manual testing we just performed into a proper
test. Even though this function is very simple, writing a test or two will
safeguard against future regressions. And who knows: we might still spot a bug!

To test CockroachDB’s SQL functionality, we use a logic test framework that
provides a convenient syntax for asserting the expected results of queries.

Take a peek at the top of one of these logic test files,
`pkg/sql/logictest/testdata/logic_test/builtin_function`. Here’s an existing test for the
length function from that file:

```text
query II
SELECT LENGTH('Hello, 世界'), LENGTH(b'Hello, 世界')
----
9 13
```

The format is relatively straightforward. `query II` means "there’s a query on
the next line that will return two *I*nteger columns; please check that that
they match what I expect". The logic test framework takes each line after the
`----` separator as an expected row, up to the first non-blank line, and takes
each whitespace-separated value on a line as the expected value for the
corresponding column. In the above example, we expect one row of output with
columns 9 and 13.

Let’s add a new test for our function. Create a new file,
`pkg/sql/logictest/testdata/logic_test/codelab`, with the following contents:

```text
query T
select whois('pmattis')
----
Peter Mattis
```

`query T` means the query is expected to return one column of text output.

Now, run your new logic test!

```text
$ make testlogic FILES=codelab
```

If all the tests in your file pass, the last line of output will read `PASS`.
Now, let’s add a failing test. Fill in `USERNAME`, `FIRST`, and `LAST` with your
real username, first name, and last name.

```text
query T
select whois('USERNAME')
----
FIRST LAST
```

Re-run the tests and make sure they fail. This gives us confidence that our test
will actually catch bugs, should they arise. Go back and add your name to the
end of the users map, and verify that the tests once again succeed.

Let’s add one more test for the default case. Again, remember to replace `FIRST`
and `LAST` with your own name.

```
query T
select whois()
----
Ben Darnell, Peter Mattis, Spencer Kimball, FIRST LAST
```

Run the tests once more. If they still succeed, we’re done!

Well, not quite. If you haven’t seen a failure yet, run the tests a few more
times. Eventually, you should see an error like this:

```
--- FAIL: TestLogic (0.06s)
    --- FAIL: TestLogic/default (0.05s)
        --- FAIL: TestLogic/default/codelab (0.05s)
          logic_test.go:1707:
            testdata/logic_test/codelab:1:
            expected:
                Ben Darnell, Peter Mattis, Spencer Kimball
            but found (query options: "") :
                Peter Mattis, Spencer Kimball, Ben Darnell
```

Looks like we’ve found a bug! We’re expecting names in alphabetical order (Ben,
Peter, then Spencer), but the names were output in a different order!

What might cause this? Take another look at how your built-in constructs the
output string and see if you can spot the bug.

If you get stuck, check out this [blog post about maps in Go][blog-maps].

Once you’ve found and fixed the bug, verify that the tests reliably pass. Then
check your solution against ours.

<details>
  <summary>View our solution</summary>
  <p>

  <!--
    This collapsible-section hack is very sensitive to whitespace.
    Be careful! See: https://stackoverflow.com/a/39920717/1122351
  -->

  ```diff
    "whois": makeBuiltin(defProps(),
      tree.Overload{
        Types:      tree.VariadicType{VarType: types.String},
        ReturnType: tree.FixedReturnType(types.String),
        Fn: func(ctx *tree.EvalContext, args tree.Datums) (tree.Datum, error) {
          users := map[string]string{
            "bdarnell": "Ben Darnell",
            "pmattis":  "Peter Mattis",
            "skimball": "Spencer Kimball",
          }
          if len(args) == 0 {
            args = make(tree.Datums, 0, len(users))
            for user := range users {
              args = append(args, tree.NewDString(user))
            }
  +          sort.Slice(args, func(i, j int) bool {
  +            return *args[i].(*tree.DString) < *args[j].(*tree.DString)
  +          })
          }
          var buf bytes.Buffer
          for i, arg := range args {
            name, ok := users[strings.ToLower(string(*arg.(*tree.DString)))]
            if !ok {
              return tree.DNull, fmt.Errorf("unknown username: %s", arg)
            }
            if i > 0 {
              buf.WriteString(", ")
            }
            buf.WriteString(name)
          }
          return tree.NewDString(buf.String()), nil
        },
      },
    ),
    ...
  ```
  </p>
</details>

<span></span> <!-- Force space after collapsible section. -->

That’s it! You’ve successfully added a bug-free built-in SQL function to
CockroachDB.

[CONTRIBUTING.md]: https://github.com/cockroachdb/cockroach/blob/master/CONTRIBUTING.md
[built-ins]: https://www.cockroachlabs.com/docs/stable/functions-and-operators.html#built-in-functions
[blog-maps]: https://blog.golang.org/go-maps-in-action#TOC_7.
