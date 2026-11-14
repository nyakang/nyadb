package engines

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// The native renderer preserves placement and storage, but mislabels defaults
// as generated expressions, leaves distribution names and comments unescaped,
// and omits sequences, triggers and RLS. Repair exact catalog-bound fragments
// and supplement its statements from the same original locked snapshot.
func (s *Session) openTenBaseDefinitionExtras(ctx context.Context, q queryer, o contract.ObjectRef, ddl string) (string, error) {
	var relations []contract.ObjectRef
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE tree AS (
 SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2
 UNION SELECT i.inhrelid FROM pg_inherits i JOIN tree t ON t.oid=i.inhparent)
 SELECT n.nspname,c.relname FROM tree JOIN pg_class c ON c.oid=tree.oid JOIN pg_namespace n ON n.oid=c.relnamespace ORDER BY c.oid LIMIT 1001`, o.Schema, o.Name)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var schema, name string
		if err = rows.Scan(&schema, &name); err != nil {
			break
		}
		if len(relations) >= 1000 {
			err = errors.New("OpenTenBase definition exceeds the 1000-relation limit")
			break
		}
		ref := o
		ref.Schema, ref.Name = schema, name
		relations = append(relations, ref)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return "", err
	}
	var before, after []string
	used := len(ddl)
	push := func(target *[]string, value string) error {
		used += len(value) + 1
		if used > 1<<20 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return errors.New("complete OpenTenBase definition exceeds 1 MiB or contains invalid text")
		}
		*target = append(*target, value)
		return nil
	}
	repair := func(native, corrected string) error {
		count := strings.Count(ddl, native)
		if count == 0 && strings.Count(ddl, corrected) == 1 {
			return nil // A newer native renderer may already emit the correct form.
		}
		if count != 1 {
			return errors.New("missing or ambiguous OpenTenBase native definition fragment")
		}
		used += len(corrected) - len(native)
		if used > 1<<20 || !utf8.ValidString(corrected) || strings.ContainsRune(corrected, 0) {
			return errors.New("complete OpenTenBase definition exceeds 1 MiB or contains invalid text")
		}
		ddl = strings.Replace(ddl, native, corrected, 1)
		return nil
	}
	for _, ref := range relations {
		var kind string
		var partition bool
		if err := queryOne(ctx, q, `SELECT c.relkind::text,c.relispartition FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, []any{ref.Schema, ref.Name}, &kind, &partition); err != nil {
			return "", err
		}
		// Child definitions inherit columns and placement from their parent.
		// The native renderer prints neither of these fragments for a child.
		if !partition {
			if kind == "r" || kind == "p" {
				policy, err := s.readDistributedPolicy(ctx, q, ref)
				if err != nil {
					return "", err
				}
				if policy == nil {
					return "", errors.New("OpenTenBase definition distribution is unavailable")
				}
				if len(policy.Columns) != 0 {
					corrected, err := policy.Clause()
					if err != nil {
						return "", err
					}
					mode := map[string]string{"S": "SHARD", "H": "HASH", "M": "MODULO"}[policy.Code]
					if mode == "" {
						return "", contract.ErrUnsupported
					}
					if err := repair("\nDISTRIBUTE BY "+mode+"("+strings.Join(policy.Columns, ", ")+")", "\n"+corrected); err != nil {
						return "", err
					}
				}
			}
			rows, err := q.QueryContext(ctx, `SELECT format(E'\n    %I %s',a.attname,format_type(a.atttypid,a.atttypmod))||
 CASE WHEN a.attcollation<>0 AND a.attcollation<>ty.typcollation THEN format(' COLLATE %I.%I',cn.nspname,coll.collname) ELSE '' END,
 pg_get_expr(d.adbin,d.adrelid,false)
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_type ty ON ty.oid=a.atttypid JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 LEFT JOIN pg_collation coll ON coll.oid=a.attcollation LEFT JOIN pg_namespace cn ON cn.oid=coll.collnamespace
 WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, ref.Schema, ref.Name)
			if err != nil {
				return "", err
			}
			for rows.Next() {
				var prefix, expression string
				if err = rows.Scan(&prefix, &expression); err != nil {
					break
				}
				err = repair(prefix+" GENERATED ALWAYS AS ("+expression+") STORED", prefix+" DEFAULT "+expression)
				if err != nil {
					break
				}
			}
			err = errors.Join(err, rows.Err())
			rows.Close()
			if err != nil {
				return "", err
			}
		}
		sequences, err := cloudberryOwnedSequences(ctx, q, ref)
		if err != nil {
			return "", err
		}
		for _, seq := range sequences {
			if seq.identity == "" {
				if err := push(&before, "CREATE SEQUENCE "+seq.name+" AS "+seq.dataType+" "+seq.options()+";"); err != nil {
					return "", err
				}
				if err := push(&after, "ALTER SEQUENCE "+seq.name+" OWNED BY "+qualified(ref)+"."+quote(seq.column)+";"); err != nil {
					return "", err
				}
			} else {
				mode := map[string]string{"a": "ALWAYS", "d": "BY DEFAULT"}[seq.identity]
				if mode == "" {
					return "", errors.New("unknown OpenTenBase identity semantics")
				}
				if err := push(&after, "ALTER TABLE "+qualified(ref)+" ALTER COLUMN "+quote(seq.column)+" ADD GENERATED "+mode+" AS IDENTITY (SEQUENCE NAME "+seq.name+" "+seq.options()+");"); err != nil {
					return "", err
				}
			}
		}
		// Exact native fragments are constructed from catalog values, never
		// guessed by splitting SQL at newlines or semicolons inside literals.
		rows, err := q.QueryContext(ctx, `SELECT format('COMMENT ON TABLE %I IS ',c.relname),format('COMMENT ON TABLE %I.%I IS ',n.nspname,c.relname),obj_description(c.oid,'pg_class')
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND obj_description(c.oid,'pg_class') IS NOT NULL
 UNION ALL SELECT format('COMMENT ON COLUMN %I.%I IS ',c.relname,a.attname),format('COMMENT ON COLUMN %I.%I.%I IS ',n.nspname,c.relname,a.attname),col_description(c.oid,a.attnum)
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped AND col_description(c.oid,a.attnum) IS NOT NULL
 UNION ALL SELECT format('COMMENT ON CONSTRAINT %I ON %I IS ',con.conname,c.relname),format('COMMENT ON CONSTRAINT %I ON %I.%I IS ',con.conname,n.nspname,c.relname),obj_description(con.oid,'pg_constraint')
 FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND con.contype='c' AND obj_description(con.oid,'pg_constraint') IS NOT NULL`, ref.Schema, ref.Name)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var native, qualifiedPrefix, comment string
			if err = rows.Scan(&native, &qualifiedPrefix, &comment); err != nil {
				break
			}
			fragment := "\n" + native + "'" + comment + "';"
			if strings.Count(ddl, fragment) > 1 {
				err = errors.New("ambiguous OpenTenBase native comment fragment")
				break
			}
			if strings.Contains(ddl, fragment) {
				used -= len(fragment)
				ddl = strings.Replace(ddl, fragment, "", 1)
			}
			if err = push(&after, qualifiedPrefix+cloudberryLiteral(comment)+";"); err != nil {
				break
			}
		}
		err = errors.Join(err, rows.Err())
		rows.Close()
		if err != nil {
			return "", err
		}
		rules, err := cloudberryDefinitionRows(ctx, q, `SELECT pg_get_ruledef(r.oid,true) FROM pg_rewrite r JOIN pg_class c ON c.oid=r.ev_class JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND r.rulename<>'_RETURN' ORDER BY r.rulename`, ref)
		if err != nil {
			return "", err
		}
		triggers, err := cloudberryDefinitionRows(ctx, q, `SELECT pg_get_triggerdef(t.oid,true)||'; ALTER TABLE '||format('%I.%I',n.nspname,c.relname)||
 CASE t.tgenabled WHEN 'D' THEN ' DISABLE TRIGGER ' WHEN 'R' THEN ' ENABLE REPLICA TRIGGER ' WHEN 'A' THEN ' ENABLE ALWAYS TRIGGER ' ELSE ' ENABLE TRIGGER ' END||quote_ident(t.tgname)
 FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND NOT t.tgisinternal ORDER BY t.tgname`, ref)
		if err != nil {
			return "", err
		}
		for _, definition := range append(rules, triggers...) {
			if err := push(&after, strings.TrimSuffix(definition, ";")+";"); err != nil {
				return "", err
			}
		}
		policies, err := cloudberryDefinitionRows(ctx, q, `SELECT 'CREATE POLICY '||quote_ident(p.polname)||' ON '||format('%I.%I',n.nspname,c.relname)||
 CASE WHEN p.polpermissive THEN ' AS PERMISSIVE' ELSE ' AS RESTRICTIVE' END||
 CASE p.polcmd WHEN '*' THEN ' FOR ALL' WHEN 'r' THEN ' FOR SELECT' WHEN 'a' THEN ' FOR INSERT' WHEN 'w' THEN ' FOR UPDATE' WHEN 'd' THEN ' FOR DELETE' END||
 ' TO '||(SELECT string_agg(CASE WHEN role_id=0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(role_id)) END,', ' ORDER BY ord) FROM unnest(p.polroles) WITH ORDINALITY r(role_id,ord))||
 CASE WHEN p.polqual IS NOT NULL THEN ' USING ('||pg_get_expr(p.polqual,p.polrelid,true)||')' ELSE '' END||
 CASE WHEN p.polwithcheck IS NOT NULL THEN ' WITH CHECK ('||pg_get_expr(p.polwithcheck,p.polrelid,true)||')' ELSE '' END
 FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 ORDER BY p.polname`, ref)
		if err != nil {
			return "", err
		}
		for _, policy := range policies {
			if err := push(&after, policy+";"); err != nil {
				return "", err
			}
		}
		var enabled, forced bool
		if err = queryOne(ctx, q, `SELECT c.relrowsecurity,c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, []any{ref.Schema, ref.Name}, &enabled, &forced); err != nil {
			return "", err
		}
		if enabled {
			if err := push(&after, "ALTER TABLE "+qualified(ref)+" ENABLE ROW LEVEL SECURITY;"); err != nil {
				return "", err
			}
		}
		if forced {
			if err := push(&after, "ALTER TABLE "+qualified(ref)+" FORCE ROW LEVEL SECURITY;"); err != nil {
				return "", err
			}
		}
	}
	if len(before) != 0 {
		ddl = strings.Join(before, "\n") + "\n" + ddl
	}
	if len(after) != 0 {
		ddl += "\n" + strings.Join(after, "\n")
	}
	if len(ddl) > 1<<20 {
		return "", errors.New("complete OpenTenBase definition exceeds 1 MiB")
	}
	return ddl, ctx.Err()
}
