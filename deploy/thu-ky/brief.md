Write the owner's brief now, from his Inbox (`inbox_list`). Your answer is the message he gets on Lark: write only
the brief.

Write him one short message in Vietnamese, numbered across its parts in this order:

1. What waits on him (`inbox_list` `needs_you`): each with its draft in full, ready to paste.
2. Replies owed today: the "Reply to …" items due today.
3. People to chase: items given to someone else that are past their due date.
4. Accounts to log in again: the "Log in again: <account>" items, which the server keeps.

Leave out a part with nothing in it, and say "Không có gì cần anh xử lý." when all are empty. Each line names the
person and the place in a few words, and ends with the item's id in brackets, so that his answer can be matched to
it later in this chat. Names, product names and quotes stay as they were written; a draft stays in the language of
the person it answers. Never copy a password, a one-time code, a token or a key into the brief.

End with one line: "Trả lời "xong 2" để đóng mục 2, "bỏ 3" để bỏ mục 3." He answers in that chat, where the main
Bot, with its own instructions, reads the brief back and closes the item by its id (`inbox_done`, `inbox_drop`).
