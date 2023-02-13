// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

/*
Package lang implements a language called Optgen, short for "optimizer
generator". Optgen is a domain-specific language (DSL) that provides an
intuitive syntax for defining, matching, and replacing nodes in a target
expression tree. Here is an example:

  [NormalizeEq]
  (Eq
    $left:^(Variable)
    $right:(Variable)
  )
  =>
  (Eq $right $left)

The expression above the arrow is called the "match pattern" and the expression
below the arrow is called the "replace pattern". If a node in the target
expression tree matches the match pattern, then it will be replaced by a node
that is constructed according to the replace pattern. Together, the match
pattern and replace pattern are called a "rule".
箭头上面的叫做 "match pattern" ，箭头下面的叫做"replace pattern"，
如果一个表达式树的node匹配了"match pattern"，他会根据"replace pattern" 替换为一个node。
"match pattern" 和 "replace pattern" 合起来称为 "rule"。

In addition to rules, the Optgen language includes "definitions". Each
definition names and describes one of the nodes that the target expression tree
may contain. Match and replace patterns can recognize and construct these
nodes. Here is an example:
除了rule，optgen 还包含"definitions"。每个 definitions 可能包含表达式树的nodes的 names
和 describes。 match 和 replace patterns 可以识别这些nodes。
Here is an example:

  define Eq {
    Left  Expr
    Right Expr
  }

The following sections provide more detail on the Optgen language syntax and
semantics, as well as some implementation notes.

Definitions

Optgen language input files may contain any number of definitions, in any
order. Each definition describes a node in the target expression tree. A
definition has a name and a set of "fields" which describe the node's children.
A definition may have zero fields, in which case it describes a node with zero
children, which is always a "leaf" in the expression tree.
Option 语言文件可以包含任意多多 definitions，任何顺序。
每个 definition 描述一个目标表达式树的一个 node。
一个 definition 有一个 name 和 一组 "fields" 来描述子节点。
一个 definition 可以没有 fields，用来描述该 node 没有 子节点，是一个 leaf  在表达式树中。

A field definition consists of two parts - the field's name and its type. The
Optgen parser treats the field's type as an opaque identifier; it's up to other
components to interpret it. However, typically the field type refers to either
some primitive type (like string or int), or else refers to the name of some
other operator or group of operators.
Fields 定义由两部分组成 - 字段的名称及其类型。
Optgen 解析器将字段的类型视为不透明的标识符； 由其他组件来解释它。
但是，通常字段类型指的是某种原始类型（如字符串或整数），或者指的是某些其他运算符或运算符组的名称。

Here is the syntax for an operator definition:

  define <name> {
    <field-1-name> <field-1-type>
    <field-2-name> <field-2-type>
    ...
  }

And here is an example:

  define Join {
    Left  Expr
    Right Expr
    On    Expr
  }

Definition Tags

A "definition tag" is an opaque identifier that describes some property of the
defined node. Definitions can have multiple tags or no tags at all, and the
same tag can be attached to multiple definitions. Tags can be used to group
definitions according to some shared property or logical grouping. For example,
arithmetic or boolean operators might be grouped together. Match patterns can
then reference those tags in order to match groups of nodes (see "Matching
Names" section).
"Definition tags "是描述定义节点的某些属性的不透明标识符。
定义可以有多个标签或根本没有标签，并且同一个标签可以附加到多个定义上。
标签可用于根据某些共享属性或逻辑分组对定义进行分组。
例如，算术或布尔运算符可能组合在一起。
然后匹配模式可以引用这些标签以匹配节点组（参见"matching names"部分）。

Here is the definition tagging syntax:

  [<tag-1-name>, <tag-2-name>, ...]
  define <name> {
  }

And here is an example:

  [Comparison, Inequality]
  define Lt {
    Left  Expr
    Right Expr
  }

Rules

Optgen language input files may contain any number of rules, in any order. Each
rule has a unique name and consists of a match pattern and a corresponding
replace pattern. A rule's match pattern is tested against every node in the
target expression tree, bottom-up. Each matching node is replaced by a node
constructed according to the replace pattern. The replacement node is itself
tested against every rule, and so on, until no further rules match. The order
that rules are applied depends on the order of the rules in each file, the
lexicographical ordering of files, and whether or not a rule is marked as high
or low priority as it is depicted below:
Option 语言的输入文件可以有任意数量和任意顺序的 rules。
每一个 rule 有一个唯一的名字和由 一个 match pattern 和一个相应的 replace pattern 组成。
一个 rule 的 match pattern 针对目标表达式树中的每个节点自下而上测试。
每一个 匹配到的 node 会被替换根据 replace pattern。被替换的node对自己进行测试直到没有 rules 匹配。
rules的顺序根据 每个文件的rules顺序，一个rule 被标记为高优先级和低优先级根据以下：

[InlineConstVar, Normalize, HighPriority]


Note that this is just a conceptual description. Optgen does not actually do
any of this matching or replacing itself. Other components use the Optgen
library to generate code. These components are free to match however they want,
and to replace nodes or keep the new and old nodes side-by-side (as with a
typical optimizer MEMO structure).
注意这只是一个概念描述。optgen 并不真正的做任何匹配和替换。
其他组件使用optgen库来生成代码。这些组件可以自由的匹配他们想要的，
替换nodes 或者保持新旧 nodes并存（MEMO）。

Similar to define statements, a rule may have a set of tags associated with it.
Rule tags logically group rules, and can also serve as directives to the code
generator.
与定义语句类似，一个 rules可能有一组与之关联的tags。
Rule tags 对规则进行逻辑分组，也可以作为代码生成器的指令。

Here is the partial rule syntax (see Syntax section for full syntax):

  [<rule-name>, <tag-1-name>, <tag-2-name>, ...]
  (<match-opname>
    <match-expr>
    <match-expr>
    ...
  )
  =>
  (<replace-opname>
    <replace-expr>
    <replace-expr>
    ...
  )

Match Patterns

The top-level match pattern matches the name and children of one or more nodes
in the target expression tree. For example:
顶层的 match pattern 匹配名字和一个或多个子nodes 子目标表达式树。

  (Eq * *)

The "*" character is the "wildcard matcher", which matches a child of any kind.
Therefore, this pattern matches any node named "Eq" that has at least two
children. Matchers can be nested within one another in order to match children,
grandchildren, etc. For example:
" * " 字符是通配符，匹配任何类型的孩子。因此，这个模式匹配 "Eq" 至少有两个孩子。
匹配可以被嵌套在另一个 children，grandchildren。例如：

  (Eq (Variable) (Const))

This pattern matches an "Eq" node with a "Variable" node as its left child
and a "Const" node as its right child.
这个 pattern 匹配一个 带有 "Variable" 左孩子和 "Const"的右孩子。

Binding

Child patterns within match and replace patterns can be "bound" to a named
variable. These variables can then be referenced later in the match pattern or
in the replace pattern. This is a critical part of the Optgen language, since
virtually every pattern constructs its replacement pattern based on parts of
the match pattern. For example:
匹配和替换模式中的子模式可以“绑定”到命名变量。
然后可以稍后在匹配模式或替换模式中引用这些变量。
这是 Optgen 语言的关键部分，因为几乎每个模式都基于匹配模式的部分构造其替换模式。 例如：

  [EliminateSelect]
  (Select $input:* (True)) => $input

The $input variable is bound to the first child of the "Select" node. If the
second child is a "True" node, then the "Select" node will be replaced by its
input. Variables can also be passed as arguments to custom matchers, which are
described below.
$input 变量绑定到“Select”节点的第一个子节点。
如果第二个子节点是“True”节点，则“Select”节点将被其输入替换。
变量也可以作为参数传递给自定义匹配器，如下所述。

Let Expression

A let expression can be used for binding multiple variables to the result of a
custom function with multiple return values. This expression consists of two
elements, a binding and a result. The binding includes a list of variables to
bind followed by a custom function to produce the bind values. The result is a
variable reference which is the value of the expression when evaluated.
let 表达式可用于将多个变量绑定到具有多个返回值的自定义函数的结果。
这个表达式由两个元素组成，一个绑定和一个结果。
绑定包括要绑定的变量列表，后跟用于生成绑定值的自定义函数。 结果是一个变量引用，它是计算时表达式的值。

For example:

  [SplitSelect]
  (Select
    $input:*
    $filters:* &
      (Let ($filterA $filterB $ok):(SplitFilters $filters) $ok)
  )
  =>
  (Select (Select $input $filterA) $filterB)

The "($filtersA $filtersB $ok):(SplitFilters $filters)" part indicates that
$filtersA $filtersB and $ok are bound to the three return values of
(SplitFilters $filters). The let expression evaluates to the value of $ok.

A let expression can also be used in a replace pattern. For example:

  [AlterSelect]
  (Select $input:* $filters:*)
  =>
  (Select
    (Let ($newInput $newFilters):(AlterSelect $input $filters) $newInput)
    $newFilters
  )

Matching Names

In addition to simple name matching, a node matcher can match tag names. Any
node type which has the named tag is matched. For example:

  [Inequality]
  define Lt {
    Left Expr
    Right Expr
  }

  [Inequality]
  define Gt
  {
    Left Expr
    Right Expr
  }

  (Inequality (Variable) (Const))

This pattern matches either "Lt" or "Gt" nodes. This is useful for writing
patterns that apply to multiple kinds of nodes, without need for duplicate
patterns that only differ in matched node name.
此模式匹配“Lt”或“Gt”节点。 这对于编写适用于多种节点的模式很有用，
而不需要仅在匹配的节点名称上不同的重复模式。

The node matcher also supports multiple names in the match list, separated by
'|' characters. The node's name is allowed to match any of the names in the
list. For example:
节点匹配器还支持匹配列表中的多个名称，用'|'分隔 人物。 允许节点的名称与列表中的任何名称匹配。 例如：

  (Eq | Ne | Inequality)

This pattern matches "Eq", "Ne", "Lt", or "Gt" nodes.

Matching Primitive Types

String and numeric constant nodes in the tree can be matched against literals.
A literal string or number in a match pattern is interpreted as a matcher of
that type, and will be tested for equality with the child node. For example:
树中的字符串和数字常量节点可以与文字进行匹配。 匹配模式中的文字字符串或数字被解释为该类型的匹配器，
并将测试与子节点是否相等。 例如：

  [EliminateConcat]
  (Concat $left:* (Const "")) => $left

If Concat's right operand is a constant expression with the empty string as its
value, then the pattern matches. Similarly, a constant numeric expression can be
matched like this:
如果 Concat 的右操作数是一个以空字符串为值的常量表达式，则模式匹配。 类似地，一个常量数值表达式可以这样匹配：

  [LimitScan]
  (Limit (Scan $def:*) (Const 1)) => (ScanOneRow $def)

Matching Lists

Nodes can have a child that is a list of nodes rather than a single node. As an
example, a function call node might have two children: the name of the function
and the list of arguments to the function:
节点可以有一个子节点，它是节点列表而不是单个节点。
例如，函数调用节点可能有两个子节点：函数的名称和函数的参数列表：

  define FuncCall {
    Name Expr
    Args ExprList
  }

There are several kinds of list matchers, each of which uses a variant of the
list matching bracket syntax. The ellipses signify that 0 or more items can
match at either the beginning or end of the list. The item pattern can be any
legal match pattern, and can be bound to a variable.
有几种列表匹配器，每种都使用列表匹配括号语法的变体。
省略号表示 0 个或多个项目可以在列表的开头或结尾匹配。
项目模式可以是任何合法的匹配模式，并且可以绑定到一个变量。

  [ ... <item pattern> ... ]

- ANY: Matches if any item in the list matches the item pattern. If multiple
items match, then the list matcher binds to the first match.

  [ ... $item:* ... ]

- FIRST: Matches if the first item in the list matches the item pattern (and
there is at least one item in the list).

  [ $item:* ... ]

- LAST: Matches if the last item in the list matches the item pattern (and
there is at least one item).

  [ ... $item:* ]

- SINGLE: Matches if there is exactly one item in the list, and it matches the
item pattern.

  [ $item:* ]

- EMPTY: Matches if there are zero items in the list.

  []

Following is a more complete example. The ANY list matcher in the example
searches through the Filter child's list, looking for a Subquery node. If a
matching node is found, then the list matcher succeeds and binds the node to
the $item variable.
下面是一个更完整的例子。 示例中的 ANY 列表匹配器搜索 Filter 子列表，寻找 Subquery 节点。
如果找到匹配节点，则列表匹配器成功并将该节点绑定到 $item 变量。

  (Select
    $input:*
    (Filter [ ... $item:(Subquery) ... ])
  )

Custom Matching

When the supported matching syntax is insufficient, Optgen provides an escape
mechanism. Custom matching functions can invoke Go functions, passing
previously bound variables as arguments, and checking the boolean result for a
match. For example:
当支持的匹配语法不足时，Optgen 提供了转义机制。
自定义匹配函数可以调用 Go 函数，将先前绑定的变量作为参数传递，并检查布尔结果是否匹配。 例如：

  [EliminateFilters]
  (Filters $items:* & (IsEmptyList $items)) => (True)

This pattern passes the $items child node to the IsEmptyList function. If that
returns true, then the pattern matches.
此模式将 $items 子节点传递给 IsEmptyList 函数。 如果返回 true，则模式匹配。

Custom matching functions can appear anywhere that other matchers can, and can
be combined with other matchers using boolean operators (see the Boolean
Expressions section for more details). While variable references are the most
common argument, it is also legal to nest function invocations:
自定义匹配函数可以出现在其他匹配器可以出现的任何位置，
并且可以使用布尔运算符与其他匹配器组合（有关详细信息，请参阅布尔表达式部分）。
虽然变量引用是最常见的参数，但嵌套函数调用也是合法的：

  (Project
    $input:*
    $projections:* & ^(IsEmpty (FindUnusedColumns $projections))
  )

Boolean Expressions

Multiple match expressions of any type can be combined using the boolean &
(AND) operator. All must match in order for the overall match to succeed:
可以使用布尔 & (AND) 运算符组合任何类型的多个匹配表达式。 所有必须匹配才能使整体匹配成功：

  (Not
    $input:(Comparison) & (Inequality) & (CanInvert $input)
  )

The boolean ^ (NOT) operator negates the result of a boolean match expression.
It can be used with any kind of matcher, including custom match functions:
布尔 ^ (NOT) 运算符否定布尔匹配表达式的结果。
它可以与任何类型的匹配器一起使用，包括自定义匹配函数：

  (JoinApply
    $left:^(Select)
    $right:* & ^(IsCorrelated $right $left)
    $on:*
  )

This pattern matches only if the left child is not a Select node, and if the
IsCorrelated custom function returns false.
仅当左子节点不是 Select 节点并且 IsCorrelated 自定义函数返回 false 时，此模式才匹配。

Replace Patterns

Once a matching node is found, the replace pattern produces a single
substitution node. The most common replace pattern involves constructing one or
more new nodes, often with child nodes that were bound in the match pattern.
A construction expression specifies the name of the node as its first operand
and its children as subsequent arguments. Construction expressions can be
nested within one another to any depth. For example:
一旦找到匹配节点，替换模式就会生成一个替换节点。
最常见的替换模式涉及构造一个或多个新节点，通常带有绑定在匹配模式中的子节点。

构造表达式将节点的名称指定为其第一个操作数，将其子节点指定为后续参数。
构造表达式可以相互嵌套到任意深度。 例如：
  [HoistSelectExists]
  (Select
    $input:*
    $filter:(Exists $subquery:*)
  )
  =>
  (SemiJoinApply
    $input
    $subquery
    (True)
  )

The replace pattern in this rule constructs a new SemiJoinApply node, with its
first two children bound in the match pattern. The third child is a newly
constructed True node.

The replace pattern can also consist of a single variable reference, in the
case where the substitution node was already present in the match pattern:
此规则中的替换模式构造了一个新的 SemiJoinApply 节点，其前两个子节点绑定在匹配模式中。
第三个孩子是一个新建的 True 节点。
如果替换节点已经存在于匹配模式中，则替换模式也可以包含单个变量引用：

  [EliminateAnd]
  (And $left:* (True)) => $left

Custom Construction

When Optgen syntax cannot easily produce a result, custom construction
functions allow results to be derived in Go code. If a construction
expression's name is not recognized as a node name, then it is assumed to be
the name of a custom function. For example:
当 Optgen 语法不能轻易产生结果时，自定义构造函数允许在 Go 代码中派生结果。
如果构造表达式的名称未被识别为节点名称，则假定它是自定义函数的名称。 例如：

  [MergeSelectJoin]
  (Select
    (InnerJoin $r:* $s:* $on:*)
    $filter:*
  )
  =>
  (InnerJoin
    $r
    $s
    (ConcatFilters $on $filter)
  )

Here, the ConcatFilters custom function is invoked in order to concatenate two
filter lists together. Function parameters can include nodes, lists (see the
Constructing Lists section), operator names (see the Name parameters section),
and the results of nested custom function calls. While custom functions
typically return a node, they can return other types if they are parameters to
other custom functions.
在这里，调用 ConcatFilters 自定义函数以将两个过滤器列表连接在一起。
函数参数可以包括节点、列表（参见构造列表部分）、运算符名称（参见名称参数部分）以及嵌套自定义函数调用的结果。
虽然自定义函数通常返回一个节点，但如果它们是其他自定义函数的参数，它们可以返回其他类型。

Constructing Lists

Lists can be constructed and passed as parameters to node construction
expressions or custom replace functions. A list consists of multiple items that
can be of any parameter type, including nodes, strings, custom function
invocations, or lists. Here is an example:
可以构造列表并将其作为参数传递给节点构造表达式或自定义替换函数。
一个列表由多个项目组成，这些项目可以是任何参数类型，包括节点、字符串、自定义函数调用或列表。 这是一个例子

  [MergeSelectJoin]
  (Select
    (InnerJoin $left:* $right:* $on:*)
    $filters:*
  )
  =>
  (InnerJoin
    $left
    $right
    (And [$on $filters])
  )

Dynamic Construction

Sometimes the name of a constructed node can be one of several choices. The
built-in "OpName" function can be used to dynamically construct the right kind
of node. For example:
有时，构造节点的名称可以是多种选择之一。 内置的“OpName”函数可用于动态构建正确类型的节点。 例如：

  [NormalizeVar]
  (Eq | Ne
    $left:^(Variable)
    $right:(Variable)
  )
  =>
  ((OpName) $right $left)

In this pattern, the name of the constructed result is either Eq or Ne,
depending on which is matched. When the OpName function has no arguments, then
it is bound to the name of the node matched at the top-level. The OpName
function can also take a single variable reference argument. In that case, it
refers to the name of the node bound to that variable:
在此模式中，构造结果的名称是 Eq 或 Ne，具体取决于匹配的名称。
当 OpName 函数没有参数时，它会绑定到顶层匹配的节点的名称。
OpName 函数也可以采用单个变量引用参数。 在这种情况下，它指的是绑定到该变量的节点的名称：

  [PushDownSelect]
  (Select
    $input:(Join $left:* $right:* $on:*)
    $filter:* & ^(IsCorrelated $filter $right)
  )
  =>
  ((OpName $input)
    (Select $left $filter)
    $right
    $on
  )

In this pattern, Join is a tag that refers to a group of nodes. The replace
expression will construct a node having the same name as the matched join node.
在此模式中，Join 是一个引用一组节点的标记。 替换表达式将构造一个与匹配的连接节点同名的节点。

Name Parameters

The OpName built-in function can also be a parameter to a custom match or
replace function which needs to know which name matched. For example:
OpName 内置函数也可以是自定义匹配或替换函数的参数，该函数需要知道哪个名称匹配。 例如：

  [FoldBinaryNull]
  (Binary $left:* (Null) & ^(HasNullableArgs (OpName)))
  =>
  (Null)

The name of the matched Binary node (e.g. Plus, In, Contains) is passed to the
HasNullableArgs function as a symbolic identifier. Here is an example that uses
a custom replace function and the OpName function with an argument:
匹配的 Binary 节点的名称（例如 Plus、In、Contains）作为符号标识符传递给 IsCalledOnNullInput 函数。
这是一个使用自定义替换函数和带有参数的 OpName 函数的示例：

  [NegateComparison]
  (Not $input:(Comparison $left:* $right:*))
  =>
  (InvertComparison (OpName $input) $left $right)

As described in the previous section, adding the argument enables OpName to
return a name that was matched deeper in the pattern.
如上一节所述，添加参数使 OpName 能够返回在模式中匹配得更深的名称。

In addition to a name returned by the OpName function, custom match and replace
functions can accept literal operator names as parameters. The Minus operator
name is passed as a parameter to two functions in this example:
除了 OpName 函数返回的名称之外，自定义匹配和替换函数还可以接受文字运算符名称作为参数。
在此示例中，减号运算符名称作为参数传递给两个函数：

  [FoldMinus]
  (UnaryMinus
    (Minus $left $right) & (OverloadExists Minus $right $left)
  )
  =>
  (ConstructBinary Minus $right $left)

Type Inference

Expressions in both the match and replace patterns are assigned a data type
that describes the kind of data that will be returned by the expression. These
types are inferred using a combination of top-down and bottom-up type inference
rules. For example:
匹配和替换模式中的表达式都分配了一个数据类型，该数据类型描述了表达式将返回的数据类型。
这些类型是使用自上而下和自下而上类型推理规则的组合来推断的。 例如

  define Select {
    Input  Expr
    Filter Expr
  }

  (Select $input:(LeftJoin | RightJoin) $filter:*) => $input

The type of $input is inferred as "LeftJoin | RightJoin" by bubbling up the type
of the bound expression. That type is propagated to the $input reference in the
replace pattern. By contrast, the type of the * expression is inferred to be
"Expr" using a top-down type inference rule, since the second argument to the
Select operator is known to have type "Expr".
$input 的类型通过冒泡绑定表达式的类型推断为“LeftJoin | RightJoin”。
该类型传播到替换模式中的 $input 引用。 相比之下，使用自上而下的类型推断规则将 * 表达式的类型推断为“Expr”，
因为已知 Select 运算符的第二个参数具有“Expr”类型。

When multiple types are inferred for an expression using different type
inference rules, the more restrictive type is assigned to the expression. For
example:
当使用不同的类型推断规则为表达式推断出多个类型时，将更严格的类型分配给表达式。 例如：

  (Select $input:* & (LeftJoin)) => $input

Here, the left input to the And expression was inferred to have type "Expr" and
the right input to have type "LeftJoin". Since "LeftJoin" is the more
restrictive type, the And expression and the $input binding are typed as
"LeftJoin".
在这里，And 表达式的左侧输入被推断为“Expr”类型，而右侧输入被推断为“LeftJoin”类型。
由于“LeftJoin”是限制性更强的类型，And 表达式和 $input 绑定被键入为“LeftJoin”。

Type inference detects and reports type contradictions, which occur when
multiple incompatible types are inferred for an expression. For example:
类型推断检测并报告类型矛盾，当为一个表达式推断出多个不兼容的类型时，就会发生这种矛盾。 例如：

  (Select $input:(InnerJoin) & (LeftJoin)) => $input

Because the input cannot be both an InnerJoin and a LeftJoin, Optgen reports a
type contradiction error.
因为输入不能同时是 Inner Join 和 Left Join，所以 Optgen 报告类型矛盾错误。

Syntax

This section describes the Optgen language syntax in a variant of extended
Backus-Naur form. The non-terminals correspond to methods in the parser. The
terminals correspond to tokens returned by the scanner. Whitespace and
comment tokens can be freely interleaved between other tokens in the
grammar.
本节以扩展的 Backus-Naur 形式描述 Optgen 语言语法。
非终结符对应于解析器中的方法。 终端对应于扫描仪返回的令牌。
空格和注释标记可以在语法中的其他标记之间自由交错。

  root         = tags (define | rule)
  tags         = '[' IDENT (',' IDENT)* ']'

  define       = 'define' define-name '{' define-field* '}'
  define-name  = IDENT
  define-field = field-name field-type
  field-name   = IDENT
  field-type   = IDENT

  rule         = func '=>' replace
  match        = func
  replace      = func | ref
  func         = '(' func-name arg* ')'
  func-name    = names | func
  names        = name ('|' name)*
  arg          = bind and | ref | and
  and          = expr ('&' and)
  expr         = func | not | let | list | any | name | STRING | NUMBER
  not          = '^' expr
  list         = '[' list-child* ']'
  list-child   = list-any | arg
  list-any     = '...'
  bind         = '$' label ':' and
  let          = '(' 'Let' '(' '$' label ('$' label)* ')' ':' func ref ')'
  ref          = '$' label
  any          = '*'
  name         = IDENT
  label        = IDENT

Here are the pseudo-regex definitions for the lexical tokens that aren't
represented as single-quoted strings above:
以下是上面未表示为单引号字符串的词法标记的伪正则表达式定义：

  STRING     = " [^"\n]* "
  NUMBER     = UnicodeDigit+
  IDENT      = (UnicodeLetter | '_') (UnicodeLetter | '_' | UnicodeNumber)*
  COMMENT    = '#' .* \n
  WHITESPACE = UnicodeSpace+

The support directory contains syntax coloring files for several editors,
including Vim, TextMate, and Visual Studio Code. JetBrains editor (i.e. GoLand)
can also import TextMate bundles to provide syntax coloring.
支持目录包含几个编辑器的语法着色文件，包括 Vim、TextMate 和 Visual Studio Code。
JetBrains 编辑器（即 GoLand）还可以导入 TextMate 包以提供语法着色。

Components

The lang package contains a scanner that breaks input files into lexical
tokens, a parser that assembles an abstract syntax tree (AST) from the tokens,
and a compiler that performs semantic checks and creates a rudimentary symbol
table.
lang 包包含一个将输入文件分解为词法标记的扫描器、一个从标记组装抽象语法树 (AST) 的解析器，
以及一个执行语义检查并创建基本符号表的编译器。

The compiled rules and definitions become the input to a separate code
generation package which generates parts of the Cockroach DB SQL optimizer.
However, the Optgen language itself is not Cockroach or SQL specific, and can
be used in other contexts. For example, the Optgen language parser generates
its own AST expressions using itself (compiler bootstrapping).
编译的规则和定义成为单独代码生成包的输入，该包生成 Cockroach DB SQL 优化器的一部分。
但是，Optgen 语言本身并不是 Cockroach 或 SQL 特定的，并且可以在其他上下文中使用。
例如，Optgen 语言解析器使用自己生成自己的 AST 表达式（编译器引导）。
*/
package lang
