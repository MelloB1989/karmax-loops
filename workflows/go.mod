// The first-party workflows, as their own module.
//
// They used to live in the KARMAX repo, which meant shipping a new workflow
// required shipping a new daemon. They build against KARMAX's guest SDK and
// nothing else in it.
module github.com/MelloB1989/karmax-loops/workflows

go 1.26

require github.com/MelloB1989/karmax v0.0.0-00010101000000-000000000000

// Built against the working tree while both repos move together. Dropped once
// KARMAX tags a release carrying the current guest SDK.
replace github.com/MelloB1989/karmax => /home/nikhil/code/KARMAX
