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
	// Listened for on the document rather than form by form, so that forms
	// that arrive later — the candidate inspector — are asked about too.
	document.addEventListener('submit', function (event) {
		var form = event.target;
		if (!form.matches || !form.matches('form[data-confirm]')) { return; }
		if (!window.confirm(form.getAttribute('data-confirm'))) {
			event.preventDefault();
			event.stopImmediatePropagation();
		}
	}, true);

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

	// --- Talking to the server without leaving the page ---------------------
	// Redirects are never followed: a session that has run out answers with a
	// redirect to the sign-in page, and following it would come back 200 with
	// that page and look exactly like a save.
	var post = function (url, body) {
		body.set('tyst', '1');
		return fetch(url, {
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

	// --- Drafts of the interview team's notes --------------------------------
	// Whatever is typed into a note is kept in this browser until the server
	// has it, so a failed save, a closed tab or an expired session never costs
	// anybody an interview's worth of writing.
	var draftKey = function (id) { return 'utkast:kandidat:' + id + ':anteckning'; };
	var draft = {
		get: function (id) {
			try { return window.localStorage.getItem(draftKey(id)); } catch (e) { return null; }
		},
		set: function (id, text) {
			try { window.localStorage.setItem(draftKey(id), text); } catch (e) { /* nothing to do */ }
		},
		drop: function (id) {
			try { window.localStorage.removeItem(draftKey(id)); } catch (e) { /* nothing to do */ }
		}
	};

	// --- The candidate board -------------------------------------------------
	// Everything here is a shortcut for something the page can already do
	// without it: the stage selector on each card moves a candidate, the add
	// button is a link to a real page, and the fields have a form of their
	// own at /kandidater. This makes those three quicker, and steps out of
	// the way the moment anything fails.
	(function board() {
		var root = document.querySelector('[data-board]');
		if (!root || !window.fetch) { return; }

		// Moving a card is the one change made on the board itself; the rest
		// is done in the inspector.
		var save = function (id, field, value) {
			var body = new URLSearchParams();
			body.set('falt', field);
			body.set('varde', value);
			return post('/kandidater/' + encodeURIComponent(id) + '/falt', body);
		};

		var counts = function () {
			root.querySelectorAll('[data-lane]').forEach(function (lane) {
				var n = lane.querySelectorAll('[data-card]').length;
				var tag = lane.querySelector('[data-lane-count]');
				if (tag) { tag.textContent = n; }
			});
		};

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
		// The whole lane is the target, header and all, so that a folded
		// lane still takes a card.
		root.addEventListener('dragover', function (event) {
			if (!dragging) { return; }
			var lane = event.target.closest('[data-drop]');
			if (!lane) { return; }
			event.preventDefault();
			event.dataTransfer.dropEffect = 'move';
			root.querySelectorAll('.is-over').forEach(function (l) { l.classList.remove('is-over'); });
			lane.classList.add('is-over');
		});
		root.addEventListener('drop', function (event) {
			var lane = event.target.closest('[data-drop]');
			if (!lane || !dragging) { return; }
			event.preventDefault();
			lane.classList.remove('is-over');

			var card = dragging;
			var list = lane.querySelector('.lane-cards');
			var stage = lane.getAttribute('data-lane');
			var from = card.parentElement;
			if (from === list) { return; }

			list.appendChild(card);
			counts();

			card.classList.add('is-saving');
			save(card.getAttribute('data-card'), 'steg', stage).then(function () {
				card.classList.remove('is-saving');
			}).catch(function () {
				// Put it back where it came from rather than leave the board
				// showing a move that did not happen.
				from.appendChild(card);
				card.classList.remove('is-saving');
				counts();
				window.location.reload();
			});
		});

	})();

	// --- Reading the board, for everybody who can see it -------------------
	// Finished lanes fold away so the live ones get the room. Without this
	// every lane stays open.
	(function boardView() {
		var board = document.querySelector('.board');
		if (!board) { return; }
		var key = function (lane) { return 'kandidater:fold:' + lane.getAttribute('data-lane'); };
		var remembered = function (lane) {
			try { return window.localStorage.getItem(key(lane)); } catch (e) { return null; }
		};
		var fold = function (lane, folded, keep) {
			lane.classList.toggle('is-folded', folded);
			var button = lane.querySelector('[data-fold]');
			if (button) {
				button.textContent = button.getAttribute(folded ? 'data-unfold-label' : 'data-fold-label');
				button.setAttribute('aria-expanded', folded ? 'false' : 'true');
			}
			if (keep) {
				try { window.localStorage.setItem(key(lane), folded ? '1' : '0'); } catch (e) { /* nothing to do */ }
			}
		};
		// A note that was written but never saved is pointed out on its card,
		// so that it is found and finished rather than forgotten.
		board.querySelectorAll('[data-card]').forEach(function (card) {
			if (draft.get(card.getAttribute('data-card')) === null) { return; }
			card.classList.add('has-draft');
			card.setAttribute('data-draft-label', board.getAttribute('data-msg-draft') || '');
		});

		board.querySelectorAll('[data-foldable]').forEach(function (lane) {
			var button = lane.querySelector('[data-fold]');
			if (!button) { return; }
			button.hidden = false;
			fold(lane, remembered(lane) !== '0', false);
			button.addEventListener('click', function () {
				fold(lane, !lane.classList.contains('is-folded'), true);
			});
		});
	})();

	// --- The candidate inspector -------------------------------------------
	// A card opens its own page in a panel over the board: every field, the
	// whole note with room to write, and the actions. Without script, or if
	// anything here fails, the link simply goes to that page.
	(function inspector() {
		if (!window.fetch) { return; }
		var dialog = document.getElementById('inspektor');
		if (dialog && (typeof dialog.showModal !== 'function' || !window.DOMParser)) { dialog = null; }
		// The same page on its own, opened from a link or without the board.
		var standalone = dialog ? null : document.querySelector('[data-inspector]');
		if (!dialog && !standalone) { return; }
		var slot = dialog ? dialog.querySelector('[data-inspector-slot]') : null;
		var dirty = false;
		var article = standalone;

		var msg = function (name) { return article ? article.getAttribute('data-msg-' + name) || '' : ''; };
		var grow = function (box) {
			if (!box) { return; }
			box.style.height = 'auto';
			box.style.height = Math.min(box.scrollHeight + 2, window.innerHeight * 0.6) + 'px';
		};
		var mark = function (id) {
			// The address says which card is open, so a reload reopens it.
			var url = window.location.pathname + window.location.search + (id ? '#kort-' + id : '');
			try { window.history.replaceState(null, '', url); } catch (e) { /* nothing to do */ }
		};

		var close = function (force) {
			if (!force && dirty && !window.confirm(msg('discard'))) { return false; }
			if (dirty && article) { draft.drop(article.getAttribute('data-id')); }
			dirty = false;
			dialog.close();
			return true;
		};

		var wire = function () {
			dirty = false;
			var form = article.querySelector('[data-inspector-form]');
			var id = article.getAttribute('data-id');
			if (dialog) {
				article.querySelectorAll('[data-inspector-close]').forEach(function (link) {
					link.addEventListener('click', function (event) { event.preventDefault(); close(false); });
				});
			}
			if (!form) { return; }
			var note = form.querySelector('[data-draft]');
			var status = form.querySelector('[data-status]');
			var button = form.querySelector('button[type="submit"]');
			var say = function (text, kind) {
				status.textContent = text;
				status.className = 'inspector-status' + (kind ? ' is-' + kind : '');
			};

			var kept = note ? draft.get(id) : null;
			if (kept !== null) {
				if (kept !== note.value) {
					note.value = kept;
					dirty = true;
					say(msg('restored'), 'warn');
				} else {
					draft.drop(id);
				}
			}
			grow(note);

			form.addEventListener('input', function (event) {
				dirty = true;
				if (event.target === note) {
					draft.set(id, note.value);
					grow(note);
				}
			});
			if (note) {
				note.addEventListener('keydown', function (event) {
					if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
						event.preventDefault();
						if (form.requestSubmit) { form.requestSubmit(); } else { form.dispatchEvent(new Event('submit', { cancelable: true })); }
					}
				});
			}

			form.addEventListener('submit', function (event) {
				event.preventDefault();
				button.disabled = true;
				say('…');
				post(form.action, new URLSearchParams(new FormData(form))).then(function () {
					draft.drop(id);
					dirty = false;
					say(msg('saved'), 'ok');
					// The card's column, preview and counts all follow from
					// what was saved; the page is the simplest way to show it.
					if (dialog) {
						mark('');
						window.location.reload();
					} else {
						window.location.href = '/kandidater';
					}
				}).catch(function (err) {
					button.disabled = false;
					if (note) { draft.set(id, note.value); }
					say(err && err.signedOut ? msg('signedout') : msg('failed'), 'error');
					if (err && err.signedOut) {
						var link = document.createElement('a');
						link.href = '/login';
						link.target = '_blank';
						link.rel = 'noopener';
						link.textContent = msg('signin');
						status.appendChild(document.createTextNode(' '));
						status.appendChild(link);
					}
				});
			});
		};

		var open = function (href, id) {
			fetch(href, { credentials: 'same-origin', redirect: 'manual' }).then(function (r) {
				if (!r.ok) { throw new Error('status ' + r.status); }
				return r.text();
			}).then(function (html) {
				var found = new DOMParser().parseFromString(html, 'text/html').querySelector('[data-inspector]');
				if (!found) { throw new Error('no inspector'); }
				slot.replaceChildren(document.importNode(found, true));
				article = slot.querySelector('[data-inspector]');
				wire();
				if (!dialog.open) { dialog.showModal(); }
				// Straight to the note for the team, at its end, ready to
				// add to; anybody else starts at the top.
				var note = article.querySelector('[data-draft]');
				grow(note);
				if (note) {
					note.focus({ preventScroll: true });
					note.setSelectionRange(note.value.length, note.value.length);
				}
				dialog.scrollTop = 0;
				mark(id);
			}).catch(function () { window.location.href = href; });
		};

		window.addEventListener('beforeunload', function (event) {
			if (dirty) { event.preventDefault(); event.returnValue = ''; }
		});
		if (standalone) {
			wire();
			return;
		}

		document.addEventListener('click', function (event) {
			var link = event.target.closest && event.target.closest('a[data-inspect]');
			if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) { return; }
			event.preventDefault();
			var card = link.closest('[data-card]');
			open(link.getAttribute('href'), card ? card.getAttribute('data-card') : '');
		});

		// Every way out asks first when something is unsaved: the ×, Escape,
		// a click beside the panel and the cancel button.
		dialog.querySelector('.sheet-close').addEventListener('submit', function (event) {
			event.preventDefault();
			close(false);
		});
		dialog.addEventListener('cancel', function (event) {
			event.preventDefault();
			close(false);
		});
		dialog.addEventListener('click', function (event) {
			if (event.target === dialog) { close(false); }
		});
		dialog.addEventListener('close', function () {
			slot.replaceChildren();
			article = null;
			mark('');
		});
		var wanted = /^#kort-([A-Za-z0-9_-]+)$/.exec(window.location.hash);
		if (wanted) { open('/kandidater/' + wanted[1], wanted[1]); }
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
