# codist-fix

Prepares fixes for the issues [Codist](https://github.com/AbiXnash/codist) found, for a person to approve.

- **Propose:** the local model writes small edits to the current code for one issue, with why. A developer can say
  how they want it fixed; a suggestion about anything else (another class, a new feature) is refused.
- **Check:** the fixed code is built and its tests run with Maven in a separate copy, so the working tree is never
  touched.
- **Apply:** only after a person approves, and only the fix's own files are committed. It refuses if the branch moved
  or the files changed since the fix was prepared.
- **Remember:** why a fix was rejected is kept and shown to the model next time.

`Routes` registers the requests the interface's fix dialog sends: `POST /api/fix`, `POST /api/fix/{id}/check`,
`POST /api/fix/{id}/apply`, `DELETE /api/fix/{id}` and `POST /api/memory`.

## Licence

Source available under the [PolyForm Noncommercial License 1.0.0](LICENSE): free for any noncommercial purpose, such
as personal study, research, hobby projects and testing, and for use by charities, schools, public research and
public health bodies and government institutions. **Using it for a business, or in anything sold, needs a commercial
licence** from the author: open an issue at [github.com/AbiXnash/codist](https://github.com/AbiXnash/codist/issues)
to ask. Third-party code included in Codist keeps its own licence.

Required Notice: Copyright 2026 Abinash (https://github.com/AbiXnash)
