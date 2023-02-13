// Copyright 2017 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

/*
Package fsm provides an interface for defining and working with finite-state
machines.
fsm 包提供了一个用于定义和使用有限状态机的接口。

The package is split into two main types: Transitions and Machine. Transitions
is an immutable State graph with Events acting as the directed edges between
different States. The graph is built by calling Compile on a Pattern, which is
meant to be done at init time. This pattern is a mapping from current States to
Events that may be applied on those states to resulting Transitions. The pattern
supports pattern matching on States and Events using wildcards and variable
bindings. To add new transitions to the graph, simply adjust the Pattern
provided to Compile. Transitions are not used directly after creation, instead,
they're used by Machine instances.
该包分为两种主要类型：Transitions 和 Machine。
Transitions 是一个不可变的状态图，事件充当不同状态之间的有向边。
该图是通过在模式上调用编译来构建的，这意味着在初始时间完成。
此模式是从当前状态到事件的映射，可以将这些映射应用于这些状态以生成转换。
该模式支持使用通配符和变量绑定对状态和事件进行模式匹配。
要向图形添加新的转换，只需调整提供给编译的模式。 转换不会在创建后直接使用，而是由 Machine 实例使用。

Machine is an instantiation of a finite-state machine. It is given a Transitions
graph when it is created to specify its State graph. Since the Transition graph
is itself state-less, multiple Machines can be powered by the same graph
simultaneously. The Machine has an Apply(Event) method, which applies the
provided event to its current state. This does two things:
Machine 是有限状态机的一个实例。 它在创建时被赋予一个 Transitions 图以指定其状态图。
由于 Transition 图本身是无状态的，因此多个机器可以同时由同一个图供电。
Machine 有一个 Apply(Event) 方法，它将提供的事件应用到它的当前状态。 这做了两件事：
1. It may move the current State to a new State, according to the Transitions
   graph.
1. 根据转换图，它可以将当前状态移动到新状态。
2. It may apply an Action function on the Machine's ExtendedState, which is
   extra state in a Machine that does not contribute to state transition
   decisions, but that can be affected by a state transition.
2. 它可以在机器的 ExtendedState 上应用 Action 函数，它是机器中的额外状态，
   不会影响状态转换决策，但会受到状态转换的影响。

See example_test.go for a full working example of a state machine with an
associated set of states and events.
请参阅 example_test.go 以获取具有一组关联的状态和事件的状态机的完整工作示例。

This package encourages the Pattern to be declared as a map literal. When
declaring this literal, be careful to not declare two equal keys: they'll result
in the second overwriting the first with no warning because of how Go deals with
map literals. Note that keys that are not technically equal, but where one is a
superset of the other, will work as intended. E.g. the following is permitted:
这个包鼓励将 Pattern 声明为地图文字。 声明此文字时，注意不要声明两个相等的键：
由于 Go 处理地图文字的方式，它们会导致第二个键覆盖第一个键而没有警告。
请注意，技术上不相等的密钥，但其中一个是另一个的超集，将按预期工作。 例如。 以下是允许的：
 Compile(Pattern{
   stateOpen{retryIntent: Any} {
     eventTxnFinish{}: {...}
   }
   stateOpen{retryIntent: True} {
     eventRetriableErr{}: {...}
   }

Members of this package are accessed frequently when implementing a state
machine. For that reason, it is encouraged to dot-import this package in the
file with the transitions Pattern. The respective file should be kept small and
named <name>_fsm.go; our linter doesn't complain about dot-imports in such
files.
在实现状态机时经常访问此包的成员。 出于这个原因，鼓励使用转换模式在文件中点导入此包。
相应的文件应保持较小并命名为 <name>_fsm.go； 我们的 linter 不会抱怨此类文件中的点导入。

*/
package fsm
