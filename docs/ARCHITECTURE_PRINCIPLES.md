# Architecture Principles (Mandatory)

This project follows strict architectural rules. These are HARD RULES, not
suggestions. If new or existing code violates them, flag it and refactor it
rather than working around it. Do not silently comply with a request that
would introduce a violation — point it out and propose the compliant
alternative first.

## 1. Composition over Inheritance
- Default to composition. Only use inheritance/subclassing when you need
  BOTH hierarchical code reuse AND subtype polymorphism (i.e. the subtype
  must be substitutable wherever the base type is expected, per the
  Liskov Substitution Principle).
- If you only want code reuse, extract a shared component/helper and
  compose it in, don't subclass for it.
- If you only want polymorphism, use an interface/abstract type, not a
  base class with shared implementation.
- Before introducing any inheritance, state explicitly which of the two
  conditions apply. If only one applies, reject the inheritance approach
  and use composition or an interface instead.

## 2. Depend on Abstractions, not Concretions
- Code that uses a dependency should depend on an interface/abstract type
  describing the behavior it needs, never directly on a concrete
  implementation.
- This applies in both directions: the consumer references the
  abstraction, and the concrete implementation lives behind it, swappable
  without touching the consumer.
- Concrete types are acceptable only at the lowest-level "leaf" of the
  dependency graph (e.g. a single concrete adapter implementing an
  interface) — never as something another component reaches into directly.

## 3. Dependency Injection over Instantiation
- A component must never construct (direct instantiation, global
  singleton access) the concrete dependencies it needs internally.
- Dependencies are passed in (constructor injection preferred; setter/method
  injection only when justified) as their abstract type.
- Wiring/construction of concrete implementations happens in one place only
  — a composition root / entrypoint / bootstrap layer — never scattered
  through business logic.
- This applies to: database clients, message bus clients, HTTP clients,
  exchange/broker clients, clocks/time sources, loggers, config sources —
  anything that talks to the outside world or could plausibly be swapped,
  mocked, or faked in a test.

## Why
These three rules exist together because they reinforce each other:
abstractions are what make injected dependencies swappable, and injection
is what makes composition (rather than inheritance) practical without
constructors becoming a mess. The test for "did I do this right" is: can I
swap any concrete dependency for a fake/mock in a unit test without
modifying the component itself? If not, something is wired wrong.

## Practical checklist when writing or reviewing code
- [ ] Does this component construct any of its own dependencies?
      → Move to injection.
- [ ] Does this code reference a concrete type where it only needs certain
      behavior? → Extract/use an interface.
- [ ] Is inheritance used here for reuse only, or polymorphism only?
      → Replace with composition or an interface respectively.
- [ ] Could I unit test this by injecting fakes with zero code changes to
      the component itself? → If no, fix the wiring.

## When generating or modifying code in this repo
1. Identify what abstraction(s) the new code depends on.
2. If the abstraction doesn't exist yet, define it first (interface) based
   on what the consumer needs, not what the implementation happens to
   expose.
3. Implement the concrete type separately, satisfying that abstraction.
4. Wire it up only at the composition root.
5. If you find existing code that violates this (a service instantiating
   its own client directly, a class hierarchy used only for code reuse,
   etc.), call it out explicitly and propose a refactor rather than
   extending the violation further.
