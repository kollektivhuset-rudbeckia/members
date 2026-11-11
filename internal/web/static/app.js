// Small progressive enhancements. Every page works without any of this: the
// filter box submits, the sort links navigate, and the forms post.
(function () {
	'use strict';

	// --- Theme toggle -------------------------------------------------------
	var root = document.documentElement;
	var stored = null;
	try { stored = localStorage.getItem('rb-theme'); } catch (e) { /* private mode */ }
	if (stored === 'light' || stored === 'dark') {
		root.setAttribute('data-theme', stored);
	}
	var toggle = document.querySelector('[data-theme-toggle]');
	if (toggle) {
		toggle.addEventListener('click', function () {
			var dark = root.getAttribute('data-theme') === 'dark' ||
				(root.getAttribute('data-theme') !== 'light' &&
					window.matchMedia('(prefers-color-scheme: dark)').matches);
			var next = dark ? 'light' : 'dark';
			root.setAttribute('data-theme', next);
			try { localStorage.setItem('rb-theme', next); } catch (e) { /* ignore */ }
		});
	}

	// --- Ask before anything irreversible -----------------------------------
	document.querySelectorAll('form[data-confirm]').forEach(function (form) {
		form.addEventListener('submit', function (event) {
			if (!window.confirm(form.getAttribute('data-confirm'))) {
				event.preventDefault();
			}
		});
	});

	// --- Reveal the "something else" box only when it is wanted -------------
	// The box is in the page whether or not this runs: hiding it by default
	// would leave somebody without JavaScript told to fill in a field they
	// cannot see. All this does is collapse it until it is the answer.
	(function reasonOther() {
		var reveal = document.querySelector('[data-reveals]');
		if (!reveal) { return; }
		var box = document.querySelector(reveal.getAttribute('data-reveals'));
		if (!box) { return; }
		var sync = function () {
			box.hidden = !reveal.checked;
		};
		reveal.addEventListener('change', sync);
		sync();
	})();

	// --- Search that answers as you type ------------------------------------
	// The form still works on its own: this only saves the round trip through
	// a page load. The matching itself stays on the server, so the browser
	// never has a second opinion about what "matches" means — one register of
	// a hundred people filtered two different ways is how a row goes missing.
	document.querySelectorAll('form[data-live-search]').forEach(function (form) {
		var target = document.querySelector(form.getAttribute('data-live-search'));
		if (!target || !window.fetch || !window.AbortController) { return; }

		var timer = null, inflight = null, lastURL = null;

		var show = function (html) {
			var doc = new DOMParser().parseFromString(html, 'text/html');
			var fresh = doc.querySelector(form.getAttribute('data-live-search'));
			if (!fresh) { return; }
			target.innerHTML = fresh.innerHTML;
		};

		var run = function () {
			var url = form.action + '?' + new URLSearchParams(new FormData(form)).toString();
			if (url === lastURL) { return; }
			lastURL = url;
			if (inflight) { inflight.abort(); }
			inflight = new AbortController();
			target.setAttribute('aria-busy', 'true');
			fetch(url, {
				signal: inflight.signal,
				headers: { 'X-Requested-With': 'fetch' },
				credentials: 'same-origin'
			}).then(function (r) {
				return r.ok ? r.text() : Promise.reject(r.status);
			}).then(function (html) {
				show(html);
				// The address bar follows, so a reload or a shared link gives
				// the same list back.
				window.history.replaceState(null, '', url);
			}).catch(function (err) {
				// An aborted request is the next keystroke, not a failure.
				if (err && err.name === 'AbortError') { return; }
				// Anything else: leave the page as it was and let the form
				// work the way it does without this.
				lastURL = null;
			}).then(function () {
				target.setAttribute('aria-busy', 'false');
			});
		};

		// Typing is debounced; picking from a menu is not, because that is a
		// finished decision rather than a half-typed word.
		form.querySelectorAll('input[type="search"], input[type="text"]').forEach(function (box) {
			box.addEventListener('input', function () {
				window.clearTimeout(timer);
				timer = window.setTimeout(run, 120);
			});
		});
		form.querySelectorAll('select').forEach(function (menu) {
			menu.addEventListener('change', run);
		});
		// Enter would otherwise reload the page to reach the list it is
		// already showing.
		form.addEventListener('submit', function (event) {
			event.preventDefault();
			window.clearTimeout(timer);
			run();
		});
	});

	// --- Follow the membership the form is set to ---------------------------
	// The apartment only means anything for somebody who lives here, and
	// "also in" offers the groups a member is not already in by virtue of
	// their kind — offering their own would be a second way to say the same
	// thing. Without JavaScript both stay visible, and the server ignores the
	// combinations that make no sense.
	var kind = document.querySelector('[data-kind-select]');
	if (kind) {
		var only = document.querySelectorAll('[data-only-kind]');
		var notKind = document.querySelectorAll('[data-not-kind]');
		var sync = function () {
			only.forEach(function (field) {
				field.hidden = field.getAttribute('data-only-kind') !== kind.value;
			});
			notKind.forEach(function (field) {
				var same = field.getAttribute('data-not-kind') === kind.value;
				field.hidden = same;
				// A hidden tick must not be submitted: changing somebody's
				// kind would otherwise leave them "also in" the group they
				// have just moved out of.
				if (same) {
					field.querySelectorAll('input[type="checkbox"]').forEach(function (box) {
						box.checked = false;
					});
				}
			});
		};
		kind.addEventListener('change', sync);
		sync();
	}

	// --- The candidate board -------------------------------------------------
	// Everything here is a shortcut for something the page can already do
	// without it: the stage selector on each card moves a candidate, the add
	// button is a link to a real page, and the fields have a form of their
	// own at /kandidater. This makes those three quicker, and steps out of
	// the way the moment anything fails.
	(function board() {
		var root = document.querySelector('[data-board]');
		if (!root || !window.fetch) { return; }

		// Ask the server to change one field, and say whether it took.
		//
		// Redirects are not followed: a session that has run out answers
		// with a redirect to the sign-in page, and following it would come
		// back 200 with the sign-in page and look exactly like a save.
		var save = function (id, field, value) {
			var body = new URLSearchParams();
			body.set('falt', field);
			body.set('varde', value);
			body.set('tyst', '1');
			return fetch('/kandidater/' + encodeURIComponent(id) + '/falt', {
				method: 'POST',
				headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
				body: body.toString(),
				redirect: 'manual',
				credentials: 'same-origin'
			}).then(function (r) {
				if (r.type === 'opaqueredirect' || r.status === 0 || (r.status >= 300 && r.status < 400)) {
					var e = new Error('signed out'); e.signedOut = true; throw e;
				}
				if (!r.ok) { throw new Error('status ' + r.status); }
			});
		};

		var msg = function (name) { return root.getAttribute('data-msg-' + name) || ''; };

		// --- Drafts --------------------------------------------------------
		// Whatever is typed into a long field is kept in this browser until
		// the server has it, so a failed save, a closed tab or an expired
		// session never costs anybody an interview's worth of notes.
		var draftKey = function (id, field) { return 'utkast:kandidat:' + id + ':' + field; };
		var draft = {
			get: function (id, field) {
				try { return window.localStorage.getItem(draftKey(id, field)); } catch (e) { return null; }
			},
			set: function (id, field, text) {
				try { window.localStorage.setItem(draftKey(id, field), text); } catch (e) { /* nothing to do */ }
			},
			drop: function (id, field) {
				try { window.localStorage.removeItem(draftKey(id, field)); } catch (e) { /* nothing to do */ }
			}
		};
		var storedText = function (cell) {
			var raw = cell.getAttribute('data-raw');
			if (raw !== null) { return raw; }
			var shown = cell.textContent.trim();
			return shown === '—' ? '' : shown;
		};
		// Point out drafts left over from last time, so they are found.
		root.querySelectorAll('[data-type="multiline"]').forEach(function (cell) {
			var kept = draft.get(cell.getAttribute('data-id'), cell.getAttribute('data-edit'));
			if (kept === null) { return; }
			if (kept.trim() === storedText(cell)) {
				draft.drop(cell.getAttribute('data-id'), cell.getAttribute('data-edit'));
				return;
			}
			cell.classList.add('has-draft');
			cell.setAttribute('data-draft-label', msg('draft'));
		});

		var counts = function () {
			root.querySelectorAll('[data-lane]').forEach(function (lane) {
				var n = lane.querySelectorAll('[data-card]').length;
				var tag = lane.querySelector('[data-lane-count]');
				if (tag) { tag.textContent = n; }
			});
		};

		// --- Click a field to change it ---------------------------------
		// One field at a time. Opening eleven inputs to correct a telephone
		// number is what this replaces.
		var editing = null;
		root.addEventListener('click', function (event) {
			if (event.target.closest('.inline-status a')) { return; }
			var cell = event.target.closest('[data-edit]');
			if (!cell || cell === editing || cell.querySelector('input, textarea')) { return; }
			if (editing) { return; }

			var field = cell.getAttribute('data-edit');
			var id = cell.getAttribute('data-id');
			var type = cell.getAttribute('data-type') || 'text';
			var multiline = type === 'multiline';
			var before = storedText(cell);

			var input;
			if (multiline) {
				input = document.createElement('textarea');
				input.rows = 4;
				var kept = draft.get(id, field);
				input.value = kept !== null ? kept : before;
			} else {
				input = document.createElement('input');
				input.type = type;
				input.value = before;
			}
			var max = cell.getAttribute('data-max');
			if (max) { input.maxLength = parseInt(max, 10); }
			input.className = 'inline-input';
			input.setAttribute('aria-label', cell.getAttribute('data-label') || field);

			// A long text is written below its label, across the whole card,
			// with a line saying how it is saved and room for an error.
			var row = cell.parentElement;
			var status = null;
			editing = cell;
			cell.classList.remove('has-draft');
			cell.textContent = '';
			cell.appendChild(input);
			if (multiline) {
				row.classList.add('is-writing');
				status = document.createElement('small');
				status.className = 'inline-status';
				status.textContent = msg('hint');
				cell.appendChild(status);
			}
			var grow = function () {
				if (!multiline) { return; }
				input.style.height = 'auto';
				input.style.height = (input.scrollHeight + 2) + 'px';
			};
			grow();
			input.focus();
			if (multiline) {
				input.setSelectionRange(input.value.length, input.value.length);
			} else {
				input.select();
			}

			input.addEventListener('input', function () {
				grow();
				if (multiline) { draft.set(id, field, input.value); }
			});

			var busy = false;
			var close = function (text) {
				editing = null;
				row.classList.remove('is-writing');
				cell.textContent = text === '' ? '—' : text;
			};
			var finish = function (keep) {
				if (busy || editing !== cell) { return; }
				var value = keep ? input.value.trim() : before;

				if (!keep || value === before) {
					if (multiline) { draft.drop(id, field); }
					close(before);
					return;
				}

				busy = true;
				cell.classList.remove('is-failed');
				cell.classList.add('is-saving');
				save(id, field, value).then(function () {
					busy = false;
					if (multiline) { draft.drop(id, field); }
					// The card is dragged and read, not re-rendered, so the
					// displayed form of a date is worked out here rather than
					// asking the server for the whole page again.
					if (cell.hasAttribute('data-raw')) { cell.setAttribute('data-raw', value); }
					before = value;
					close(value);
					cell.classList.remove('is-saving');
					cell.classList.add('is-saved');
					setTimeout(function () { cell.classList.remove('is-saved'); }, 1200);
				}).catch(function (err) {
					busy = false;
					cell.classList.remove('is-saving');
					cell.classList.add('is-failed');
					if (multiline) {
						// Keep the editor open with the text in it. Throwing
						// away what somebody wrote because the network
						// hiccuped is the one thing this must never do.
						draft.set(id, field, input.value);
						status.textContent = err && err.signedOut ? msg('signedout') : msg('failed');
						if (err && err.signedOut) {
							var link = document.createElement('a');
							link.href = '/login';
							link.target = '_blank';
							link.rel = 'noopener';
							link.textContent = ' → /login';
							status.appendChild(link);
						}
						return;
					}
					close(before);
					// A change that did not save must not look as though it
					// did; reload so the card shows what is actually stored.
					setTimeout(function () { window.location.reload(); }, 900);
				});
			};

			input.addEventListener('blur', function () { finish(true); });
			input.addEventListener('keydown', function (e) {
				if (e.key === 'Enter' && (!multiline || e.ctrlKey || e.metaKey)) {
					e.preventDefault(); finish(true);
				}
				if (e.key === 'Escape') {
					e.preventDefault();
					if (multiline && input.value.trim() !== before && !window.confirm(msg('discard'))) { return; }
					finish(false);
				}
			});
		});

		// A card must not start dragging because somebody selected text in a
		// field they are editing.
		root.addEventListener('mousedown', function (event) {
			var card = event.target.closest('[data-card]');
			if (!card) { return; }
			card.draggable = !event.target.closest('[data-edit], input, select, button, a');
		});

		// --- Drag between lanes -------------------------------------------
		var dragging = null;
		root.addEventListener('dragstart', function (event) {
			var card = event.target.closest('[data-card]');
			if (!card) { return; }
			dragging = card;
			card.classList.add('is-dragging');
			event.dataTransfer.effectAllowed = 'move';
			// Firefox will not start a drag without something on the payload.
            event.dataTransfer.setData('text/plain', card.getAttribute('data-card'));
		});
		root.addEventListener('dragend', function () {
			if (dragging) { dragging.classList.remove('is-dragging'); }
			dragging = null;
			root.querySelectorAll('.is-over').forEach(function (l) { l.classList.remove('is-over'); });
		});
		root.addEventListener('dragover', function (event) {
			if (!dragging) { return; }
			var list = event.target.closest('[data-drop]');
			if (!list) { return; }
			event.preventDefault();
			event.dataTransfer.dropEffect = 'move';
			root.querySelectorAll('.is-over').forEach(function (l) { l.classList.remove('is-over'); });
			list.closest('[data-lane]').classList.add('is-over');
		});
		root.addEventListener('drop', function (event) {
			var list = event.target.closest('[data-drop]');
			if (!list || !dragging) { return; }
			event.preventDefault();

			var card = dragging;
			var lane = list.closest('[data-lane]');
			var stage = lane.getAttribute('data-lane');
			var from = card.parentElement;
			if (from === list) { return; }

			list.appendChild(card);
			counts();

			// Keep the card's own selector honest, so the no-script path and
			// the dragged position never disagree.
			var select = card.querySelector('[data-move]');
			if (select) { select.value = stage; }

			card.classList.add('is-saving');
			save(card.getAttribute('data-card'), 'steg', stage).then(function () {
				card.classList.remove('is-saving');
			}).catch(function () {
				// Put it back where it came from rather than leave the board
				// showing a move that did not happen.
				from.appendChild(card);
				if (select) { select.value = from.closest('[data-lane]').getAttribute('data-lane'); }
				card.classList.remove('is-saving');
				counts();
				window.location.reload();
			});
		});

		// --- The stage selector, without a submit button --------------------
		root.querySelectorAll('[data-move]').forEach(function (select) {
			select.addEventListener('change', function () {
				var card = select.closest('[data-card]');
				var target = root.querySelector('[data-lane="' + select.value + '"] [data-drop]');
				save(card.getAttribute('data-card'), 'steg', select.value).then(function () {
					if (target) { target.appendChild(card); counts(); }
				}).catch(function () { select.form.submit(); });
			});
		});
	})();

	// --- Close the account menu the way people expect ----------------------
	// It opens and closes on its own without this; all the script adds is
	// clicking elsewhere, and Escape.
	document.querySelectorAll('[data-menu]').forEach(function (menu) {
		document.addEventListener('click', function (event) {
			if (menu.open && !menu.contains(event.target)) { menu.open = false; }
		});
		menu.addEventListener('keydown', function (event) {
			if (event.key === 'Escape' && menu.open) {
				menu.open = false;
				var summary = menu.querySelector('summary');
				if (summary) { summary.focus(); }
			}
		});
	});

	// --- Open a form over the page instead of navigating to it --------------
	document.querySelectorAll('[data-open-dialog]').forEach(function (link) {
		var dialog = document.querySelector(link.getAttribute('data-open-dialog'));
		if (!dialog || typeof dialog.showModal !== 'function') { return; }
		link.addEventListener('click', function (event) {
			event.preventDefault();
			dialog.showModal();
			var first = dialog.querySelector('input, select, textarea');
			if (first) { first.focus(); }
		});
		// Clicking the backdrop closes it, which is what everybody expects.
		dialog.addEventListener('click', function (event) {
			if (event.target === dialog) { dialog.close(); }
		});
	});

	// --- Open the editor when the page was sent back with an error ----------
	if (window.location.hash === '#andra') {
		var editor = document.getElementById('andra');
		if (editor && editor.scrollIntoView) {
			editor.scrollIntoView({ block: 'start' });
		}
	}
})();
