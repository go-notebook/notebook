# The names a Julia cell defines and reads, as julia 1.12 itself resolves
# them: each top-level statement is put through REPL.softscope (the soft-scope
# rule of the REPL and IJulia, "Scope of Variables" in the manual) and lowered
# in a fresh module; a global the lowered code declares, assigns as a
# constant or defines a method of is defined, a global it reads is used.
#
# Input on stdin: snippets separated by NUL bytes. Output: one line per
# snippet, "defines<TAB>uses", each a sorted comma-separated list, uses
# without the defined names. Nothing is run: statements are only lowered, and
# the globals a statement defines are given the value nothing before the next
# one is lowered, so that the soft-scope rule sees them, as it would after
# running the statement.
using REPL

const SKIP = Set([:nothing])

name_ok(n::Symbol) = !startswith(string(n), "#") && !(n in SKIP)

function reads!(uses, x, m)
    if x isa GlobalRef
        x.mod === m && name_ok(x.name) && push!(uses, x.name)
    elseif x isa Expr
        for a in x.args
            reads!(uses, a, m)
        end
    elseif x isa Core.CodeInfo
        for st in x.code
            reads!(uses, st, m)
        end
    end
end

function defines!(defs, ci::Core.CodeInfo, m)
    for st in ci.code
        st isa Expr || continue
        h = st.head
        if h in (:globaldecl, :global, :const) && st.args[1] isa GlobalRef && st.args[1].mod === m
            name_ok(st.args[1].name) && push!(defs, st.args[1].name)
        elseif h === :method && length(st.args) == 1 && st.args[1] isa GlobalRef && st.args[1].mod === m
            name_ok(st.args[1].name) && push!(defs, st.args[1].name)
        end
    end
end

lastname(e) = e isa Expr ? lastname(e.args[end]) : e

function imports!(defs, st)
    for a in st.args
        if a isa Expr && a.head === :(:)
            for n in a.args[2:end]
                push!(defs, lastname(n))
            end
        elseif a isa Expr && a.head === :as
            push!(defs, a.args[2])
        else
            push!(defs, lastname(a))
        end
    end
end

function analyze(src)
    m = Module(:Cell)
    defs = Set{Symbol}()
    uses = Set{Symbol}()
    for st in Meta.parseall(src).args
        st isa LineNumberNode && continue
        if st isa Expr && st.head === :module
            push!(defs, st.args[2])
        elseif st isa Expr && st.head in (:using, :import)
            imports!(defs, st)
        else
            l = Meta.lower(m, REPL.softscope(st))
            if l isa Expr && l.head === :thunk
                defines!(defs, l.args[1], m)
                reads!(uses, l.args[1], m)
            elseif l isa Expr && l.head in (:error, :incomplete)
                return "error", ""
            else
                reads!(uses, l, m)
            end
        end
        for d in defs
            isdefined(m, d) || Core.eval(m, Expr(:global, Expr(:(=), d, nothing)))
        end
    end
    j(s) = join(sort!(String.(collect(s))), ",")
    return j(defs), j(setdiff(uses, defs))
end

data = read(stdin, String)
for src in split(data, '\0'; keepempty=false)
    d, u = analyze(String(src))
    println(d, "\t", u)
end
