# Using gtd

This describes the tool as it is after milestone M1. Where something is planned but
not built yet, it says so. Screen and button names are the English ones; German is in
Settings.

## The idea in one minute

Your head is for having ideas, not for holding them. gtd gives every thought exactly
two moments:

1. **Capture**: get it out of your head in five seconds, without deciding anything.
2. **Clarify**: later, at your desk, decide once what it is and where it belongs.

After that a thought is one of four things: an **action** you can do, a **project**
(an outcome that needs more than one action), a **note** (something worth keeping),
or gone. You never look at a raw thought twice.

## The daily loop

### 1. Capture, all day

Open gtd, type, press Enter. The field is empty again and nothing else happens. That
is deliberate: no counts, no suggestions, nothing to pull you in.

- One thought per capture. Half a sentence is enough: "bgp collector reset thing ask eric".
- Shift+Enter adds a line. Pasting several lines asks whether that is one item or one per line.
- The camera and paperclip buttons attach a photo or file, with or without text.
- `c` jumps to Capture from anywhere on a keyboard.
- From the terminal: `gtd add "text"`.

Do not sort, title or judge while capturing. A bad capture that exists beats a good
one you were going to write later.

### 2. Clarify, once or twice a day

**Inbox → Process inbox.** You get one capture at a time, oldest first, and a short
series of questions. Each answer is one tap, or a number key (1 for the first option).

| Question | What it decides |
|---|---|
| What is it? | Fix the title so that it will make sense in a month. |
| Is it actionable? | **Yes**, or one of three ways out: reference, someday, trash. |
| More than one step? | Yes makes it a project (a new one, or added to an existing one). |
| Next physical action? | One sentence, starting with a verb. "Call Brent about the API", not "API". |
| Under 2 minutes? | Then do it now. A timer runs; press Done. |
| Who? | You, or someone you are waiting for. |
| When and where? | Context (Calls, Errands, …), not before, target, due, energy, time. All optional. |
| Anything after that? | For projects: follow-up steps, each waiting for the one before. |

Leaving in the middle is fine. Nothing is saved until the last step, and the capture
stays at the head of the inbox. There is no "skip": every capture gets a decision.

### 3. Engage, when you have time to work

**Engage** asks where you are, how much time you have and how much energy, and shows
at most seven actions that fit. Selecting a place shows only actions for that place
plus those marked "Anywhere".

- **Done** completes an action. If something was waiting for it, that becomes "next"
  and shows up in your inbox under "From others" as unblocked.
- **Defer** hides it until a date. **Not now** hides it until you leave the page.
- **Focus** shows one action alone, with a timer and a single capture field, so a
  stray thought costs you five seconds instead of your concentration.
- When you complete a project's last open action, gtd asks for the next one. A
  project without a next action is stuck, and stuck projects are flagged everywhere.

The tabs **Waiting for** and **Due and targets** are for a quick look, not for working.

Keys: `e` Engage, `j`/`k` move, `x` done, `/` search, `f` full screen.

## Projects

A project is an outcome: "Collector survives BGP session resets", not "Collector".
**Projects** lists them as a tree. Opening one shows its **plan**:

- its sub-projects, nested, each with done/total and a stuck or slipping flag;
- its actions, with what each one waits for;
- a field to add an action directly;
- its notes, and a **thread**.

Dependencies: drag action A onto action B and A waits for B. On a phone, open A and
use "Waits for" in its details. A waiting action is "planned" and never appears in
Engage until what it waits for is done.

The **thread** is for the running commentary of a project. Type "Brent says the API
is REST, 100 requests a minute" and press Add. Each entry has **Make this an action**.
Use the thread instead of a new capture when you already know which project a thought
belongs to.

**Board** shows the same actions as columns (Inbox, Next, Planned, Waiting, Deferred,
Someday, Done) and can be narrowed to an area or a project. It is for an overview and
quick fixes. **Areas** are your areas of focus (Collectors, Home, Trading); an area
with no active project is flagged.

## Notes

A note is a Markdown document: headings, lists, tables, code, links, images, files.

- **Create one** from a project's plan with **New note**, or by answering "keep as
  reference" in Clarify.
- **Link** another item by typing `[[` and picking it by title. Pasting an item's
  address from the browser does the same. The linked item shows "Linked from".
- **Attach** images and files with the paperclip. Images appear inline.
- **New note from this** starts a note that links back to the one you are reading,
  for an idea that grows out of another.
- **Task lines**: any line written as `- [ ] call Brent` is a candidate action.

### Call and meeting notes

1. Before the call, open the project and press **New note**. Type freely during the call.
2. Whenever someone (including you) commits to something, write it as a task line:
   `- [ ] send Brent the port list`.
3. After the call, press **Process open tasks** on the note. Each task line comes up
   once: adjust the wording, choose the project (it can be a different one for each
   line), a context and dates, and press Create action, or Skip.
4. A processed line turns into a link to its action. The note now shows what was
   agreed and what became of it, and the actions show which call they came from.

## From a stream of half-formed ideas to a picture

This is the part people get wrong with any tool, so here is a method rather than a
feature list.

**The problem:** an immature idea is not an action and not yet a project. If you force
it into either, your lists fill with things you will never do. If you leave it in the
inbox, the inbox stops being trustworthy. If you drop it into a pile of loose notes,
you never see it again.

**The method: every idea gets a home, and homes are themes you keep coming back to.**

1. **Capture it raw.** One line. Do not develop it while you are busy with something else.

2. **In Clarify, be honest at "Is it actionable?"** For an immature idea the answer
   is No. Then choose:
   - **Someday** if it is a possible thing to do ("try gap-fill trades on earnings days").
   - **Reference** if it is a thought, an observation or a question ("collectors keep
     failing the same way: is the session state the real problem?").
   - **Trash** more often than feels comfortable. Most ideas are noise, and the good
     ones come back.

3. **File it into a theme, not into a pile.** When Clarify asks where a reference note
   belongs, pick a project. If no project fits and this is the second or third idea
   on the same subject, type a name under "Or a new project": that creates the theme.
   Then open that project and set it to **Someday**, so that it does not count as a
   commitment and is not flagged as stuck. You now have a container that costs nothing.

4. **Let the theme's plan page be the picture.** It lists every note you filed there,
   and its thread holds the small additions. When you have a new thought about an
   existing theme, do not capture it: open the theme and add a line to its thread.

5. **Write one summary note per theme when it has a handful of ideas.** Create a note
   in the project called "Where this stands". Link the idea notes with `[[`, and write
   three things: what you now believe, what you still do not know, and what would have
   to be true for this to be worth doing. This is where separate ideas become one
   picture, and it is work only you can do today. (From M7 the tool drafts it; see below.)

6. **Promote it when it is ready.** When the summary note ends in "so I should…",
   write those as task lines, press **Process open tasks**, and set the project to
   Active. From then on it lives in Engage like everything else.

7. **Look at the Someday column once a week.** Board → Someday shows your possible
   actions; Projects with "Show finished" off shows your someday themes. Activate,
   keep or drop. Ideas that survive three looks without growing can go.

**What is missing today, plainly:**

- A note filed as "Unsorted" has no list anywhere. You find it only through Search.
  Until that is fixed, always file a reference note into a project or an area.
- The tool does not yet notice that a new idea resembles five older ones, does not
  group loose notes, and does not draft the summary. Those are M4, M5 and M7.
- The weekly review is not guided yet (M2). Step 7 above is the manual version.

## Settings

Language, theme (Light, Dark, System), your own contexts (usable in your personal
space), and templates. A template adds structured fields to notes, projects or
actions of a space, for example a "stage" on trading hypotheses that then shows as a
badge and can filter the board.

## Backups

`gtd snapshot create` takes a consistent snapshot; `gtd export <dir>` writes everything
as readable Markdown files, which you can keep in a git repository. `gtd import -restore`
rebuilds a server from such an export.
